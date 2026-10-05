package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

var (
	meter = otel.Meter(scopeName)

	serverDuration httpconv.ServerRequestDuration
	serverActive   httpconv.ServerActiveRequests
	clientDuration httpconv.ClientRequestDuration
	cacheRequests  metric.Int64Counter
)

// init creates the instruments. They use the global meter provider, so they
// record nothing until Setup registers one.
func init() {
	var errs [4]error
	serverDuration, errs[0] = httpconv.NewServerRequestDuration(meter)
	serverActive, errs[1] = httpconv.NewServerActiveRequests(meter)
	clientDuration, errs[2] = httpconv.NewClientRequestDuration(meter)
	cacheRequests, errs[3] = meter.Int64Counter("yarp.cache.requests",
		metric.WithDescription("Requests by cache result: hit, miss or bypass."),
		metric.WithUnit("{request}"))
	if err := errors.Join(errs[:]...); err != nil {
		panic(err)
	}
}

// RecordCacheResult counts a request by its cache result, such as "hit",
// and adds the result to the request's span. It does nothing when telemetry
// is off.
func (t *Telemetry) RecordCacheResult(ctx context.Context, result string) {
	if t == nil {
		return
	}
	attr := attribute.String("yarp.cache.result", result)
	cacheRequests.Add(ctx, 1, metric.WithAttributes(attr))
	trace.SpanFromContext(ctx).SetAttributes(attr)
}

// getMethodAttr returns method, or "_OTHER" for a non-standard one, which
// keeps the number of attribute values bounded.
func getMethodAttr(method string) httpconv.RequestMethodAttr {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect:
		return httpconv.RequestMethodAttr(method)
	}
	return httpconv.RequestMethodOther
}

// getPort returns u's port, or the default port of its scheme.
func getPort(u *url.URL) int {
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}
