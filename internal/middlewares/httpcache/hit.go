package httpcache

import (
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// lookup returns the stored response for r while it is fresh, or nil. A
// stale response is fetched again: yarp cannot validate it yet.
func (h *handler) lookup(r *http.Request) *record {
	varyKey, varyPlain := getVaryKey(r.Method, r)
	var vary varyRecord
	if !load(r.Context(), h.c, varyKey, &vary) || vary.Key != varyPlain {
		return nil
	}
	respKey, respPlain := getRespKey(r, vary.Names)
	var e record
	if !load(r.Context(), h.c, respKey, &e) || e.Key != respPlain || !time.Now().Before(e.ExpiresAt) {
		return nil
	}
	return &e
}

// serveHit answers r from the stored response e, with a 304 when r's
// conditions allow it.
func (h *handler) serveHit(w http.ResponseWriter, r *http.Request, e *record) {
	h.tel.RecordCacheResult(r.Context(), cacheResultHit)
	if isNotModified(r, e) {
		writeNotModified(w, e)
		return
	}
	writeEntry(w, r, e)
}

// writeEntry answers r with the stored response e.
func writeEntry(w http.ResponseWriter, r *http.Request, e *record) {
	maps.Copy(w.Header(), e.Header)
	addHitHeaders(w, e)
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		w.Write(e.Body)
	}
}

// writeNotModified answers with a 304 for e (RFC 9111 section 4.3.2).
func writeNotModified(w http.ResponseWriter, e *record) {
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
func addHitHeaders(w http.ResponseWriter, e *record) {
	w.Header().Add("Cache-Status", cacheStatusHit)
	// Age must not be negative (RFC 9111 section 5.1), even if the clock
	// has been set back since the response was stored.
	age := max(0, time.Since(e.GeneratedAt))
	w.Header().Set("Age", strconv.FormatInt(int64(age/time.Second), 10))
}

// isNotModified reports whether r's If-None-Match or, without one,
// If-Modified-Since lets the stored 200 e be answered with 304 (RFC 9111
// section 4.3.2, RFC 9110 section 13.2.2).
func isNotModified(r *http.Request, e *record) bool {
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
