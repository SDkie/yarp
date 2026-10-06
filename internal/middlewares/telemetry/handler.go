package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

// Handler returns next wrapped so each request received on entryPoint gets a
// span and metrics, or next itself when telemetry is off. next reports the
// matched route with SetRoute.
func (t *Telemetry) Handler(entryPoint string, next http.Handler) http.Handler {
	if t == nil {
		return next
	}
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
	route  *string // set by SetRoute
}

// startRequest starts the span, counts the request as active and returns
// the context to serve it with.
func (h *handler) startRequest(r *http.Request) (context.Context, serverRequest) {
	req := serverRequest{start: time.Now(), method: getMethodAttr(r.Method), route: new(string)}
	ctx, span := h.tel.startServerSpan(r, req.method, h.entryPoint)
	req.span = span
	ctx = context.WithValue(ctx, routeCtxKey{}, req.route)
	h.tel.metrics.serverActive.Add(ctx, 1, req.method, scheme, h.entryPoint)
	return ctx, req
}

// endRequest ends the span and records the request's duration. It takes rw,
// not its status, since a deferred call evaluates its arguments early.
func (h *handler) endRequest(ctx context.Context, req serverRequest, rw *responseWriter) {
	endServerSpan(req.span, rw.status, *req.route)
	h.tel.metrics.serverActive.Add(ctx, -1, req.method, scheme, h.entryPoint)
	attrs := []attribute.KeyValue{h.entryPoint, routeKey.String(*req.route)}
	if rw.status > 0 {
		attrs = append(attrs, h.tel.metrics.serverDuration.AttrResponseStatusCode(rw.status))
	}
	h.tel.metrics.serverDuration.Record(ctx, time.Since(req.start).Seconds(), req.method, scheme, attrs...)
}

// routeCtxKey holds the *string that SetRoute writes the route to.
type routeCtxKey struct{}

// SetRoute records the route that serves the request with context ctx. It
// does nothing when telemetry is off.
func SetRoute(ctx context.Context, route string) {
	if p, ok := ctx.Value(routeCtxKey{}).(*string); ok {
		*p = route
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
// when nothing was sent (such as when the client went away).
type responseWriter struct {
	http.ResponseWriter
	status int
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
