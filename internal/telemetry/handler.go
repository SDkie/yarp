package telemetry

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// Handler returns h wrapped so each request received on entryPoint gets a
// span and metrics, or h itself when telemetry is off. h reports the matched
// route with SetRoute.
func (t *Telemetry) Handler(entryPoint string, h http.Handler) http.Handler {
	if t == nil {
		return h
	}
	ep := attribute.String("yarp.entrypoint", entryPoint)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m := getMethodAttr(r.Method)
		ctx, span := startServerSpan(r, m, ep)
		serverActive.Add(ctx, 1, m, "http", ep)
		rw := &responseWriter{ResponseWriter: w}
		// Deferred, so the request is ended even if h panics.
		defer func() {
			endServerSpan(span, rw.status, rw.route)
			serverActive.Add(ctx, -1, m, "http", ep)
			attrs := []attribute.KeyValue{ep, attribute.String("yarp.route", rw.route)}
			if rw.status > 0 {
				attrs = append(attrs, serverDuration.AttrResponseStatusCode(rw.status))
			}
			serverDuration.Record(ctx, time.Since(start).Seconds(), m, "http", attrs...)
		}()
		h.ServeHTTP(rw, r.WithContext(ctx))
	})
}

// SetRoute records the route that serves the request; w must be the writer
// Handler passed in. It does nothing when telemetry is off.
func SetRoute(w http.ResponseWriter, route string) {
	if rw, ok := w.(*responseWriter); ok {
		rw.route = route
	}
}

// responseWriter records the status code sent to the client, which stays 0
// when nothing was sent (such as when the client went away), and the route.
type responseWriter struct {
	http.ResponseWriter
	status int
	route  string
}

func (rw *responseWriter) WriteHeader(code int) {
	// 1xx responses are interim; only the first final status counts.
	if rw.status == 0 && code >= 200 {
		rw.status = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap gives http.ResponseController access to the underlying writer, so
// flushing and hijacking keep working.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
