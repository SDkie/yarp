package telemetry

import (
	"context"
	"errors"
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

// Transport returns rt wrapped so each request it sends to a backend of
// route gets a span and metrics, or rt itself when telemetry is off.
func (t *Telemetry) Transport(rt http.RoundTripper, route string) http.RoundTripper {
	if t == nil {
		return rt
	}
	return &transport{tel: t, next: rt, route: routeKey.String(route)}
}

// transport records a span and metrics for each request sent to a backend
// of one route.
type transport struct {
	tel   *Telemetry
	next  http.RoundTripper
	route attribute.KeyValue
}

// RoundTrip records the backend's status code, or the error if no response
// arrived. A request the client cancelled is not counted as a backend error.
func (tr *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, req := tr.startRequest(r)
	resp, err := tr.next.RoundTrip(r)
	tr.endRequest(ctx, req, r, resp, err)
	return resp, err
}

// clientRequest is what endRequest needs from startRequest.
type clientRequest struct {
	start  time.Time
	method httpconv.RequestMethodAttr
	span   trace.Span
}

// startRequest starts the span and adds its traceparent to r's headers; r
// is the proxy's own copy of the client request, so no copy is needed.
func (tr *transport) startRequest(r *http.Request) (context.Context, clientRequest) {
	req := clientRequest{start: time.Now(), method: getMethodAttr(r.Method)}
	ctx, span := tr.tel.startClientSpan(r, req.method, tr.route)
	req.span = span
	tr.tel.propagator.Inject(ctx, propagation.HeaderCarrier(r.Header))
	return ctx, req
}

// endRequest ends the span and records the request's duration, unless the
// client cancelled it.
func (tr *transport) endRequest(ctx context.Context, req clientRequest, r *http.Request, resp *http.Response, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		req.span.End()
		return
	}
	attrs := []attribute.KeyValue{tr.route}
	if err != nil {
		endClientSpan(req.span, 0, err)
		attrs = append(attrs, semconv.ErrorType(err))
	} else {
		endClientSpan(req.span, resp.StatusCode, nil)
		attrs = append(attrs, tr.tel.metrics.clientDuration.AttrResponseStatusCode(resp.StatusCode))
	}
	tr.tel.metrics.clientDuration.Record(ctx, time.Since(req.start).Seconds(), req.method, r.URL.Hostname(), getPort(r.URL), attrs...)
}

// startClientSpan starts the span for req, which yarp sends to a backend of
// route.
func (t *Telemetry) startClientSpan(req *http.Request, method httpconv.RequestMethodAttr, route attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(req.Context(), getSpanName(method),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(string(method)),
			semconv.ServerAddress(req.URL.Hostname()),
			semconv.ServerPort(getPort(req.URL)),
			semconv.URLFull(getURLWithoutQuery(req.URL)),
			route,
		))
}

// endClientSpan records the backend's status code, or err if no response
// arrived, then ends span. A 4xx or 5xx is an error here, since the
// backend did not do what yarp asked.
func endClientSpan(span trace.Span, status int, err error) {
	switch {
	case err != nil:
		span.SetAttributes(semconv.ErrorType(err))
		span.SetStatus(codes.Error, err.Error())
	case status >= http.StatusBadRequest:
		span.SetAttributes(semconv.HTTPResponseStatusCode(status), semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		span.SetStatus(codes.Error, "")
	default:
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
	}
	span.End()
}
