package telemetry

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Transport returns rt wrapped so each request it sends to a backend of
// route gets a span and metrics, or rt itself when telemetry is off.
func (t *Telemetry) Transport(rt http.RoundTripper, route string) http.RoundTripper {
	if t == nil {
		return rt
	}
	return &transport{next: rt, route: attribute.String("yarp.route", route)}
}

type transport struct {
	next  http.RoundTripper
	route attribute.KeyValue
}

// RoundTrip records the backend's status code, or the error if no response
// arrived. A request the client cancelled is not counted as a backend error.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	m := getMethodAttr(req.Method)
	ctx, span := startClientSpan(req, m, t.route)
	// req is the proxy's own copy of the client request, so the traceparent
	// goes straight into its headers instead of a copy.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := t.next.RoundTrip(req)
	if errors.Is(err, context.Canceled) && req.Context().Err() != nil {
		span.End()
		return resp, err
	}
	attrs := []attribute.KeyValue{t.route}
	if err != nil {
		endClientSpan(span, 0, err)
		attrs = append(attrs, semconv.ErrorType(err))
	} else {
		endClientSpan(span, resp.StatusCode, nil)
		attrs = append(attrs, clientDuration.AttrResponseStatusCode(resp.StatusCode))
	}
	clientDuration.Record(ctx, time.Since(start).Seconds(), m, req.URL.Hostname(), getPort(req.URL), attrs...)
	return resp, err
}
