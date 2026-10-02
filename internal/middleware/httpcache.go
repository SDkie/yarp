package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/SDkie/yarp/internal/cache"
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

// Record is a stored response.
type Record struct {
	Key    string // plain variant key
	Status int
	Header http.Header
	Body   []byte
}

// varyRecord holds the Vary header names of the responses stored for a URL.
// It is empty when the response had no Vary header.
type varyRecord struct {
	Key   string // plain base key
	Names []string
}

// HTTPCache caches responses to GET and HEAD requests in c and replies from
// it when a response for the same method, host, path and query, and the same
// values of the request headers named in the response's Vary header, is
// stored. Other methods go straight to next.
func HTTPCache(c *cache.Cache, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		varyKey, varyPlain := getVaryKey(r)
		var vary varyRecord
		if load(c, varyKey, &vary) && vary.Key == varyPlain {
			respKey, respPlain := getRespKey(r, vary.Names)
			var e Record
			if load(c, respKey, &e) && e.Key == respPlain {
				writeEntry(w, r, &e)
				return
			}
		}

		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if !rec.storable() {
			return
		}
		names, ok := getVaryNames(rec.header)
		if !ok {
			return // Vary: * never matches a later request.
		}
		// The response first, so the Vary record never points to nothing.
		respKey, respPlain := getRespKey(r, names)
		e := &Record{Key: respPlain, Status: rec.status, Header: rec.header, Body: rec.body.Bytes()}
		if store(c, respKey, e) {
			store(c, varyKey, &varyRecord{Key: varyPlain, Names: names})
		}
	})
}

// getVaryKey returns the storage key of r's Vary record and the plain key it
// is hashed from: method, host, path and query, e.g.
// "GET example.com/products?page=2".
func getVaryKey(r *http.Request) (key, plain string) {
	plain = r.Method + " " + r.Host + r.URL.RequestURI()
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
func load(c *cache.Cache, key string, v any) bool {
	data, found, err := c.Get(key)
	if err != nil {
		slog.Error("cache read failed", "key", key, "error", err)
		return false
	}
	if !found {
		return false
	}
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		slog.Error("cache entry is corrupt", "key", key, "error", err)
		return false
	}
	return true
}

// store saves v under key without expiry and reports whether it was saved.
// Failures are logged; the response has already been sent, so they never
// affect the client.
func store(c *cache.Cache, key string, v any) bool {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		slog.Error("cache encode failed", "key", key, "error", err)
		return false
	}
	if err := c.Set(key, buf.Bytes(), 0); err != nil {
		slog.Error("cache write failed", "key", key, "error", err)
		return false
	}
	return true
}

func writeEntry(w http.ResponseWriter, r *http.Request, e *Record) {
	maps.Copy(w.Header(), e.Header)
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		w.Write(e.Body)
	}
}
