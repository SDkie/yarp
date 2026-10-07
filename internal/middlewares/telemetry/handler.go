package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

// Handler returns next wrapped so each request received on entryPoint gets a
// span and metrics; t must not be nil. next reports the matched route with
// SetRoute.
func (t *Telemetry) Handler(entryPoint string, next http.Handler) http.Handler {
	return &handler{tel: t, next: next, entryPoint: entryPointKey.String(entryPoint)}
}

// handler records a span and metrics for each request received on one entry
// point.
type handler struct {
	tel        *Telemetry
	next       http.Handler
	entryPoint attribute.KeyValue
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, req := h.startRequest(r)
	rw := &responseWriter{ResponseWriter: w}
	// Deferred, so the request is ended even if next panics.
	defer h.endRequest(ctx, req, rw)
	h.next.ServeHTTP(rw, r.WithContext(ctx))
}

// serverRequest is what endRequest needs from startRequest.
type serverRequest struct {
	start  time.Time
	method httpconv.RequestMethodAttr
	span   trace.Span
	info   *requestInfo // filled in by the handlers inside
}

// startRequest starts the span, counts the request as active and returns
// the context to serve it with.
func (h *handler) startRequest(r *http.Request) (context.Context, serverRequest) {
	req := serverRequest{start: time.Now(), method: getMethodAttr(r.Method), info: &requestInfo{}}
	ctx, span := h.tel.startServerSpan(r, req.method, h.entryPoint)
	req.span = span
	ctx = context.WithValue(ctx, infoCtxKey{}, req.info)
	h.tel.metrics.serverActive.Add(ctx, 1, req.method, scheme, h.entryPoint)
	return ctx, req
}

// endRequest records the cache result, ends the span and records the
// request's duration and overhead. It takes rw, not its status, since a
// deferred call evaluates its arguments early.
func (h *handler) endRequest(ctx context.Context, req serverRequest, rw *responseWriter) {
	h.tel.recordCacheResult(ctx, req.span, req.info.cacheResult)
	endServerSpan(req.span, rw.status, req.info.route)
	h.tel.metrics.serverActive.Add(ctx, -1, req.method, scheme, h.entryPoint)
	attrs := []attribute.KeyValue{h.entryPoint, routeKey.String(req.info.route)}
	if rw.status > 0 {
		attrs = append(attrs, h.tel.metrics.serverDuration.AttrResponseStatusCode(rw.status))
	}
	h.tel.metrics.serverDuration.Record(ctx, time.Since(req.start).Seconds(), req.method, scheme, attrs...)
	if rw.status > 0 {
		h.recordOverhead(ctx, req, rw.headersAt)
	}
}

// recordOverhead records yarp's own time for the request: from its start to
// headersAt, minus the time spent waiting for backends.
func (h *handler) recordOverhead(ctx context.Context, req serverRequest, headersAt time.Time) {
	attrs := []attribute.KeyValue{h.entryPoint, routeKey.String(req.info.route)}
	if req.info.cacheResult != "" {
		attrs = append(attrs, cacheResultKey.String(req.info.cacheResult))
	}
	overhead := headersAt.Sub(req.start) - req.info.backendTime
	h.tel.metrics.requestOverhead.Record(ctx, overhead.Seconds(), metric.WithAttributes(attrs...))
}

// requestInfo is what the handlers inside report about a request.
type requestInfo struct {
	route       string        // set by SetRoute
	cacheResult string        // set by SetCacheResult
	backendTime time.Duration // added to by the transport
}

// infoCtxKey holds the request's *requestInfo.
type infoCtxKey struct{}

// SetRoute records the route that serves the request with context ctx. It
// does nothing when telemetry is off.
func SetRoute(ctx context.Context, route string) {
	if info, ok := ctx.Value(infoCtxKey{}).(*requestInfo); ok {
		info.route = route
	}
}

// addBackendTime adds d, the time spent waiting for a backend, to the
// request with context ctx. It does nothing when telemetry is off.
func addBackendTime(ctx context.Context, d time.Duration) {
	if info, ok := ctx.Value(infoCtxKey{}).(*requestInfo); ok {
		info.backendTime += d
	}
}

// startServerSpan starts the span for a request received on the entry point
// ep. It continues the client's trace if r has a traceparent header.
func (t *Telemetry) startServerSpan(r *http.Request, method httpconv.RequestMethodAttr, ep attribute.KeyValue) (context.Context, trace.Span) {
	ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	return t.tracer.Start(ctx, getSpanName(method),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(string(method)),
			semconv.URLScheme(scheme),
			semconv.ServerAddress(r.Host),
			semconv.URLPath(r.URL.Path),
			ep,
		))
}

// endServerSpan records the response status (0 if nothing was sent) and the
// matched route, then ends span. Only a 5xx marks it as an error, since a
// 4xx is the client's fault; error.type is then the status code.
func endServerSpan(span trace.Span, status int, route string) {
	span.SetAttributes(routeKey.String(route))
	if status > 0 {
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
	}
	if status >= http.StatusInternalServerError {
		span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// responseWriter records the status code sent to the client, which stays 0
// when nothing was sent (such as when the client went away), and when it
// was sent.
type responseWriter struct {
	http.ResponseWriter
	status    int
	headersAt time.Time
}

func (rw *responseWriter) WriteHeader(code int) {
	// 1xx responses are interim; only the first final status counts.
	if rw.status == 0 && code >= 200 {
		rw.setStatus(code)
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.setStatus(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *responseWriter) setStatus(code int) {
	rw.status = code
	rw.headersAt = time.Now()
}

// Unwrap gives http.ResponseController access to the underlying writer, so
// flushing and hijacking keep working.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
