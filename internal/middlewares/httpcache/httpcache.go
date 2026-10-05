// Package httpcache is a middleware that caches backend responses
// (RFC 9111) and marks each response with a Cache-Status (RFC 9211).
package httpcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
)

// Responses are stored in two steps, because which request headers select a
// response (Vary) is only known from the response itself:
//
//	"vary:" + sha256(base)      → the Vary header names for this URL (varyRecord)
//	"resp:" + sha256(variant)   → the stored response (Record)
//
// base is the method, host, path and query; variant is base plus this
// request's values for the Vary header names (see getVaryKey, getRespKey). Hashing keeps keys short
// (Badger rejects keys over 65,000 bytes) and keeps URLs, which can carry
// tokens, out of key names. Each value stores its plain key, which is
// compared on read so a mismatch is never served.
const (
	varyPrefix = "vary:"
	respPrefix = "resp:"
)

// yarp's Cache-Status entries (RFC 9211).
const (
	cacheStatusHit    = "yarp; hit"
	cacheStatusMiss   = "yarp; fwd=miss"
	cacheStatusBypass = "yarp; fwd=bypass"
)

// Cache results, as counted in the yarp.cache.requests metric.
const (
	cacheResultHit    = "hit"
	cacheResultMiss   = "miss"
	cacheResultBypass = "bypass"
)

// Record is a stored response.
type Record struct {
	Key    string // plain variant key
	Status int
	Header http.Header
	Body   []byte

	// When the backend generated the response, as best yarp can tell, and
	// when it becomes stale (see getFreshness). The Age header of a hit is
	// the time since GeneratedAt.
	GeneratedAt time.Time
	ExpiresAt   time.Time
}

// varyRecord holds the Vary header names of the responses stored for a URL.
// It is empty when the response had no Vary header.
type varyRecord struct {
	Key   string // plain base key
	Names []string
}

// New returns next wrapped so it caches responses to GET and HEAD requests
// in c and replies from it when a response for the same method, host, path
// and query, and the same values of the request headers named in the
// response's Vary header, is stored. It follows these RFC 9111 and RFC 9211
// rules:
//
//   - Only final, complete responses are stored, never 206 or 304
//     (sections 3, 3.3, 4.3.4). Requests with Range, If-Match,
//     If-Unmodified-Since or If-Range bypass the cache.
//   - A hit answers the client's If-None-Match or If-Modified-Since with 304
//     when the stored 200 matches (section 4.3.2).
//   - Cache-Control no-store and private responses are never stored
//     (sections 3, 5.2.2), nor responses with Set-Cookie (section 7.3), nor
//     responses to requests with Authorization unless public, s-maxage or
//     must-revalidate allows it (section 3.5).
//   - Only fresh responses are served (sections 4, 4.2). Freshness comes
//     from s-maxage, max-age or Expires; responses without it, and no-cache
//     responses, are not stored, as yarp has no heuristic freshness or
//     validation yet. Stale responses are fetched again, never served, and
//     stored responses expire from c once stale.
//   - Responses served from the cache carry an Age header (section 4).
//   - A 2xx or 3xx response to an unsafe method invalidates the cached
//     GET and HEAD responses for its URL (section 4.4).
//   - Every response gets a Cache-Status entry: hit, miss or bypass (RFC
//     9211).
//
// Each request's cache result is recorded in tel.
func New(c *cache.Cache, tel *telemetry.Telemetry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodOptions, http.MethodTrace:
			// Safe, and never cached.
			tel.RecordCacheResult(r.Context(), cacheResultBypass)
			next.ServeHTTP(&recorder{ResponseWriter: w, cacheStatus: cacheStatusBypass}, r)
			return
		default:
			// Unsafe (or unknown) method: forward, then invalidate.
			tel.RecordCacheResult(r.Context(), cacheResultBypass)
			rec := &recorder{ResponseWriter: w, cacheStatus: cacheStatusBypass}
			next.ServeHTTP(rec, r)
			if rec.status >= 200 && rec.status < 400 {
				invalidate(c, r)
			}
			return
		}

		if bypassesCache(r) {
			tel.RecordCacheResult(r.Context(), cacheResultBypass)
			next.ServeHTTP(&recorder{ResponseWriter: w, cacheStatus: cacheStatusBypass}, r)
			return
		}

		varyKey, varyPlain := getVaryKey(r.Method, r)
		var vary varyRecord
		if load(r.Context(), c, varyKey, &vary) && vary.Key == varyPlain {
			respKey, respPlain := getRespKey(r, vary.Names)
			var e Record
			// A stale response is fetched again: yarp cannot validate it yet.
			if load(r.Context(), c, respKey, &e) && e.Key == respPlain && time.Now().Before(e.ExpiresAt) {
				tel.RecordCacheResult(r.Context(), cacheResultHit)
				if isNotModified(r, &e) {
					writeNotModified(w, &e)
				} else {
					writeEntry(w, r, &e)
				}
				return
			}
		}

		tel.RecordCacheResult(r.Context(), cacheResultMiss)
		requestTime := time.Now()
		var generatedAt, expiresAt time.Time
		rec := &recorder{ResponseWriter: w, cacheStatus: cacheStatusMiss}
		// shouldStore runs once, when the headers arrive; h is the
		// recorder's copy of them, which is what gets stored.
		rec.shouldStore = func(status int, h http.Header) bool {
			cc := parseCacheControl(h)
			if !isStorable(r, status, h, cc) {
				return false
			}
			// A stored response must carry a valid Date (RFC 9110 section
			// 6.6.1): without one, hits would be sent with the time they are
			// served, and with an invalid one, hits would replay it. Either
			// is replaced by the time the response was received.
			if _, err := http.ParseTime(h.Get("Date")); err != nil {
				h.Set("Date", rec.responseTime.UTC().Format(http.TimeFormat))
			}
			generatedAt, expiresAt = getFreshness(h, cc, requestTime, rec.responseTime)
			// Only responses still fresh on arrival can ever be served.
			return expiresAt.After(rec.responseTime)
		}
		next.ServeHTTP(rec, r)
		if !rec.storable() {
			return
		}
		names, _ := getVaryNames(rec.header) // Vary: * was rejected by isStorable.
		respKey, respPlain := getRespKey(r, names)
		e := &Record{
			Key:         respPlain,
			Status:      rec.status,
			Header:      rec.header,
			Body:        rec.body.Bytes(),
			GeneratedAt: generatedAt,
			ExpiresAt:   expiresAt,
		}
		ttl := time.Until(expiresAt)
		if ttl <= 0 {
			return
		}
		// Both records expire from the store once the response is stale. The
		// store counts expiry in whole seconds, so round up: rounding down
		// could drop a fresh response early. Hits still check ExpiresAt.
		ttl = ttl.Truncate(time.Second) + time.Second
		// The response first, so the Vary record never points to nothing.
		if store(r.Context(), c, respKey, e, ttl) {
			store(r.Context(), c, varyKey, &varyRecord{Key: varyPlain, Names: names}, ttl)
		}
	})
}

// bypassesCache reports whether r must skip the cache entirely: Range
// requests (yarp does not store partial content) and preconditions that
// only the origin can evaluate.
func bypassesCache(r *http.Request) bool {
	bypassHeaders := [...]string{
		"Range",
		"If-Match",
		"If-Unmodified-Since",
		"If-Range",
	}
	for _, name := range bypassHeaders {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}

// isStorable reports whether the response to r with status, header and its
// parsed Cache-Control cc may be stored.
func isStorable(r *http.Request, status int, header http.Header, cc cacheControl) bool {
	switch {
	case status == http.StatusPartialContent, status == http.StatusNotModified:
		return false // Partial or body-less: never a complete response.
	case status >= 500:
		return false // Server errors are never cached.
	}
	if _, ok := getVaryNames(header); !ok {
		return false // Vary: * never matches a later request.
	}
	if ct, _, _ := strings.Cut(header.Get("Content-Type"), ";"); strings.TrimSpace(ct) == "text/event-stream" {
		return false // An endless stream would be copied forever.
	}
	if cc.has("no-store") || cc.has("private") {
		return false
	}
	if cc.has("no-cache") {
		return false // Must be validated before each use (section 5.2.2.4).
	}
	if len(header.Values("Set-Cookie")) > 0 {
		return false // A user's cookie must not reach others (section 7.3).
	}
	if r.Header.Get("Authorization") != "" {
		return cc.has("public") || cc.has("s-maxage") || cc.has("must-revalidate")
	}
	return true
}

// invalidate drops the cached GET and HEAD responses for r's URL by
// deleting their Vary records, which makes every stored variant
// unreachable.
func invalidate(c *cache.Cache, r *http.Request) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		key, plain := getVaryKey(method, r)
		if err := c.Delete(key); err != nil {
			slog.ErrorContext(r.Context(), "cache invalidation failed", "key", plain, "error", err)
		}
	}
}

// getVaryKey returns the storage key of the Vary record for method and r's
// URL, and the plain key it is hashed from: method, host, path and query,
// e.g. "GET example.com/products?page=2".
func getVaryKey(method string, r *http.Request) (key, plain string) {
	plain = method + " " + r.Host + r.URL.RequestURI()
	return getHashKey(varyPrefix, plain), plain
}

// getRespKey returns the storage key of the response stored for r and the
// plain key it is hashed from: the Vary record's plain key plus r's values
// for the Vary header names, one "\n"-separated line each. A header r does
// not send is marked absent, so it only matches requests that also lack it.
// Header values cannot contain "\n".
func getRespKey(r *http.Request, names []string) (key, plain string) {
	base := r.Method + " " + r.Host + r.URL.RequestURI()
	var b strings.Builder
	b.WriteString(base)
	for _, name := range names {
		values := r.Header.Values(name)
		b.WriteString("\n")
		b.WriteString(name)
		if len(values) == 0 {
			b.WriteString(" absent")
			continue
		}
		b.WriteString(": ")
		b.WriteString(normalizeValues(values))
	}
	plain = b.String()
	return getHashKey(respPrefix, plain), plain
}

// getHashKey returns the storage key for a plain key: prefix plus the hex
// SHA-256 of plain, always len(prefix)+64 bytes.
func getHashKey(prefix, plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return prefix + hex.EncodeToString(sum[:])
}

// normalizeValues joins header field lines into one comma-separated value
// with the spaces around each element removed, so equivalent requests (RFC
// 9111 section 4.1) produce the same key.
func normalizeValues(values []string) string {
	var parts []string
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			parts = append(parts, strings.TrimSpace(part))
		}
	}
	return strings.Join(parts, ",")
}

// getVaryNames returns the sorted, canonical header names listed in h's Vary
// header. ok is false for "Vary: *", which means the response can never be
// reused for another request.
func getVaryNames(h http.Header) (names []string, ok bool) {
	for _, v := range h.Values("Vary") {
		for name := range strings.SplitSeq(v, ",") {
			name = strings.TrimSpace(name)
			switch name {
			case "":
				continue
			case "*":
				return nil, false
			}
			names = append(names, http.CanonicalHeaderKey(name))
		}
	}
	slices.Sort(names)
	return slices.Compact(names), true
}

// load decodes the value stored under key into v. Missing keys and read or
// decode failures (which are logged) report false.
func load(ctx context.Context, c *cache.Cache, key string, v any) bool {
	data, found, err := c.Get(key)
	if err != nil {
		slog.ErrorContext(ctx, "cache read failed", "key", key, "error", err)
		return false
	}
	if !found {
		return false
	}
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		slog.ErrorContext(ctx, "cache entry is corrupt", "key", key, "error", err)
		return false
	}
	return true
}

// store saves v under key until ttl passes and reports whether it was saved.
// Failures are logged; the response has already been sent, so they never
// affect the client.
func store(ctx context.Context, c *cache.Cache, key string, v any, ttl time.Duration) bool {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		slog.ErrorContext(ctx, "cache encode failed", "key", key, "error", err)
		return false
	}
	if err := c.Set(key, buf.Bytes(), ttl); err != nil {
		slog.ErrorContext(ctx, "cache write failed", "key", key, "error", err)
		return false
	}
	return true
}

func writeEntry(w http.ResponseWriter, r *http.Request, e *Record) {
	maps.Copy(w.Header(), e.Header)
	addHitHeaders(w, e)
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		w.Write(e.Body)
	}
}

// writeNotModified answers with a 304 for e (RFC 9111 section 4.3.2).
func writeNotModified(w http.ResponseWriter, e *Record) {
	// The stored fields a 304 carries (RFC 9110 section 15.4.5).
	notModifiedHeaders := [...]string{
		"Cache-Control",
		"Content-Location",
		"Date",
		"ETag",
		"Expires",
		"Last-Modified",
		"Vary",
	}
	for _, name := range notModifiedHeaders {
		for _, v := range e.Header.Values(name) {
			w.Header().Add(name, v)
		}
	}
	addHitHeaders(w, e)
	w.WriteHeader(http.StatusNotModified)
}

// addHitHeaders adds the Cache-Status and Age of a response served from e.
func addHitHeaders(w http.ResponseWriter, e *Record) {
	w.Header().Add("Cache-Status", cacheStatusHit)
	// Age must not be negative (RFC 9111 section 5.1), even if the clock
	// has been set back since the response was stored.
	age := max(0, time.Since(e.GeneratedAt))
	w.Header().Set("Age", strconv.FormatInt(int64(age/time.Second), 10))
}

// isNotModified reports whether r's If-None-Match or, without one,
// If-Modified-Since lets the stored 200 e be answered with 304 (RFC 9111
// section 4.3.2, RFC 9110 section 13.2.2).
func isNotModified(r *http.Request, e *Record) bool {
	if e.Status != http.StatusOK {
		return false
	}
	if inm := r.Header.Values("If-None-Match"); len(inm) > 0 {
		return matchesETag(inm, e.Header.Get("ETag"))
	}
	ims := r.Header.Values("If-Modified-Since")
	if len(ims) != 1 {
		return false
	}
	since, err := http.ParseTime(ims[0])
	if err != nil {
		return false
	}
	modified, err := http.ParseTime(e.Header.Get("Last-Modified"))
	if err != nil {
		// Without a valid Last-Modified, Date is used (RFC 9111 section 4.3.2).
		if modified, err = http.ParseTime(e.Header.Get("Date")); err != nil {
			return false
		}
	}
	return !modified.After(since)
}

// matchesETag reports whether any If-None-Match value, each a list, matches
// etag by weak comparison (RFC 9110 sections 8.8.3.2, 13.1.2).
func matchesETag(values []string, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	for _, v := range values {
		for tag := range strings.SplitSeq(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || (etag != "" && strings.TrimPrefix(tag, "W/") == etag) {
				return true
			}
		}
	}
	return false
}
