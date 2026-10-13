package httpcache

import (
	"context"
	"net/http"
	"time"

	"github.com/SDkie/yarp/internal/middlewares/telemetry"
)

// serveMiss forwards r and, when the response may be reused, stores it in
// the background so the client never waits for the write.
func (h *handler) serveMiss(w http.ResponseWriter, r *http.Request) {
	telemetry.SetCacheResult(r.Context(), cacheResultMiss)
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
		h.save(r, rec, requestTime, generatedAt, expiresAt)
	}
}

// save stores the response recorded for r, sent at requestTime, until
// expiresAt, writing it in a new goroutine.
func (h *handler) save(r *http.Request, rec *recorder, requestTime, generatedAt, expiresAt time.Time) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return
	}

	names, _ := getVaryNames(rec.header) // Vary: * was rejected by isStorable.
	respKey, respPlain := getRespKey(h.route, r, names)
	e := &record{
		Key:         respPlain,
		Status:      rec.status,
		Header:      rec.header,
		Body:        rec.body.Bytes(),
		GeneratedAt: generatedAt,
		ExpiresAt:   expiresAt,
		RequestTime: requestTime,
	}
	varyKey, varyPlain := getVaryKey(h.route, r.Method, r)
	// Keeps the trace for logs, without r's cancellation.
	ctx := context.WithoutCancel(r.Context())

	go func() {
		// An existing RequestTime is kept, so other variants stay fresh.
		var vary varyRecord
		if !load(ctx, h.s, varyKey, &vary) || vary.Key != varyPlain {
			vary = varyRecord{Key: varyPlain, RequestTime: requestTime}
		}
		if requestTime.Before(vary.RequestTime) {
			return // Already stale.
		}
		vary.Names = names
		// The response first, so the Vary record never points to nothing.
		if store(ctx, h.s, respKey, e, ttl) {
			store(ctx, h.s, varyKey, &vary, ttl)
		}
	}()
}
