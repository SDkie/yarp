package httpcache

import (
	"net/http"
	"time"
)

// serveMiss forwards r and stores the response when it may be reused.
func (h *handler) serveMiss(w http.ResponseWriter, r *http.Request) {
	h.tel.RecordCacheResult(r.Context(), cacheResultMiss)
	requestTime := time.Now()
	var generatedAt, expiresAt time.Time
	rec := &recorder{ResponseWriter: w, cacheStatus: cacheStatusMiss}
	// shouldStore runs once, when the headers arrive; header is the
	// recorder's copy of them, which is what gets stored.
	rec.shouldStore = func(status int, header http.Header) bool {
		var ok bool
		generatedAt, expiresAt, ok = checkStorable(r, status, header, requestTime, rec.responseTime)
		return ok
	}
	h.next.ServeHTTP(rec, r)
	if rec.storable() {
		h.save(r, rec, generatedAt, expiresAt)
	}
}

// save stores the response recorded for r until it becomes stale at
// expiresAt.
func (h *handler) save(r *http.Request, rec *recorder, generatedAt, expiresAt time.Time) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return
	}
	// Both records expire from the store once the response is stale,
	// rounded up to a whole second so never early. Hits still check
	// ExpiresAt.
	ttl = ttl.Truncate(time.Second) + time.Second

	names, _ := getVaryNames(rec.header) // Vary: * was rejected by isStorable.
	respKey, respPlain := getRespKey(r, names)
	e := &record{
		Key:         respPlain,
		Status:      rec.status,
		Header:      rec.header,
		Body:        rec.body.Bytes(),
		GeneratedAt: generatedAt,
		ExpiresAt:   expiresAt,
	}
	varyKey, varyPlain := getVaryKey(r.Method, r)
	// The response first, so the Vary record never points to nothing.
	if store(r.Context(), h.c, respKey, e, ttl) {
		store(r.Context(), h.c, varyKey, &varyRecord{Key: varyPlain, Names: names}, ttl)
	}
}
