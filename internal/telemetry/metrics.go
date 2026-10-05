package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
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

// StartRequest starts a span for r and counts it as active on entryPoint.
// The returned ctx carries the span. The returned end records its duration
// and status code (0 if nothing was sent).
func StartRequest(r *http.Request, entryPoint string) (ctx context.Context, end func(status int, route string)) {
	start := time.Now()
	m := getMethodAttr(r.Method)
	ctx, span := startServerSpan(r, m, entryPoint)
	ep := attribute.String("yarp.entrypoint", entryPoint)
	serverActive.Add(ctx, 1, m, "http", ep)
	return ctx, func(status int, route string) {
		endServerSpan(span, status, route)
		serverActive.Add(ctx, -1, m, "http", ep)
		attrs := []attribute.KeyValue{ep, attribute.String("yarp.route", route)}
		if status > 0 {
			attrs = append(attrs, serverDuration.AttrResponseStatusCode(status))
		}
		serverDuration.Record(ctx, time.Since(start).Seconds(), m, "http", attrs...)
	}
}

// RecordBackendRequest records a request yarp sent to server for route: its
// status code, or err if no response arrived.
func RecordBackendRequest(ctx context.Context, method string, server *url.URL, route string, status int, err error, d time.Duration) {
	attrs := []attribute.KeyValue{attribute.String("yarp.route", route)}
	if err != nil {
		attrs = append(attrs, semconv.ErrorType(err))
	} else {
		attrs = append(attrs, clientDuration.AttrResponseStatusCode(status))
	}
	clientDuration.Record(ctx, d.Seconds(), getMethodAttr(method), server.Hostname(), getPort(server), attrs...)
}

// RecordCacheResult counts a request by its cache result, such as "hit".
func RecordCacheResult(ctx context.Context, result string) {
	cacheRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("yarp.cache.result", result)))
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
