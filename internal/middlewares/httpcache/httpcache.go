package httpcache

import (
	"log/slog"
	"net/http"

	"github.com/SDkie/yarp/internal/middlewares/telemetry"
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

// New returns next wrapped with the HTTP cache: responses to GET and HEAD
// are stored in c and reused while fresh, following the rules in the
// package doc. c must not be nil; each request's cache result is recorded
// in tel.
func New(c Store, tel *telemetry.Telemetry, next http.Handler) http.Handler {
	return &handler{c: c, tel: tel, next: next}
}

// handler is the middleware returned by New.
type handler struct {
	c    Store
	tel  *telemetry.Telemetry
	next http.Handler
}

// ServeHTTP answers r from the cache (hit.go), forwards it and stores the
// response (miss.go), or forwards it without using the cache.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if bypassesCache(r) {
			h.bypass(w, r)
			return
		}
		if e := h.lookup(r); e != nil {
			h.serveHit(w, r, e)
			return
		}
		h.serveMiss(w, r)
	case http.MethodOptions, http.MethodTrace:
		h.bypass(w, r) // Safe, and never cached.
	default:
		h.serveUnsafe(w, r)
	}
}

// bypass forwards r without using the cache. It returns the recorder that
// saw the response.
func (h *handler) bypass(w http.ResponseWriter, r *http.Request) *recorder {
	h.tel.RecordCacheResult(r.Context(), cacheResultBypass)
	rec := &recorder{ResponseWriter: w, cacheStatus: cacheStatusBypass}
	h.next.ServeHTTP(rec, r)
	return rec
}

// serveUnsafe forwards r, whose method is unsafe or unknown, then
// invalidates its URL if the response is a 2xx or 3xx (section 4.4).
func (h *handler) serveUnsafe(w http.ResponseWriter, r *http.Request) {
	if rec := h.bypass(w, r); rec.status >= 200 && rec.status < 400 {
		h.invalidate(r)
	}
}

// invalidate drops the cached GET and HEAD responses for r's URL by
// deleting their Vary records, which makes every stored variant
// unreachable.
func (h *handler) invalidate(r *http.Request) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		key, plain := getVaryKey(method, r)
		if err := h.c.Delete(key); err != nil {
			slog.ErrorContext(r.Context(), "cache invalidation failed", "key", plain, "error", err)
		}
	}
}
