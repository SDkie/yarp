package telemetry

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer(scopeName)

// startServerSpan starts the span for a request received on entryPoint. It
// continues the client's trace if r has a traceparent header.
func startServerSpan(r *http.Request, method httpconv.RequestMethodAttr, entryPoint string) (context.Context, trace.Span) {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	return tracer.Start(ctx, getSpanName(method),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(string(method)),
			semconv.URLScheme("http"),
			semconv.ServerAddress(r.Host),
			semconv.URLPath(r.URL.Path),
			attribute.String("yarp.entrypoint", entryPoint),
		))
}

// endServerSpan records the response status (0 if nothing was sent) and the
// matched route, then ends span. Only a 5xx marks it as an error, since a
// 4xx is the client's fault; error.type is then the status code.
func endServerSpan(span trace.Span, status int, route string) {
	span.SetAttributes(attribute.String("yarp.route", route))
	if status > 0 {
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
	}
	if status >= http.StatusInternalServerError {
		span.SetAttributes(semconv.ErrorTypeKey.String(strconv.Itoa(status)))
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// startClientSpan starts the span for req, which yarp sends to server for
// route, and puts the span's traceparent in req's headers.
func startClientSpan(req *http.Request, method httpconv.RequestMethodAttr, server *url.URL, route string) trace.Span {
	ctx, span := tracer.Start(req.Context(), getSpanName(method),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(string(method)),
			semconv.ServerAddress(server.Hostname()),
			semconv.ServerPort(getPort(server)),
			semconv.URLFull(getURLWithoutQuery(req.URL)),
			attribute.String("yarp.route", route),
		))
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	return span
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

// getURLWithoutQuery returns u without its query, fragment and user info,
// which may hold secrets.
func getURLWithoutQuery(u *url.URL) string {
	c := *u
	c.User, c.RawQuery, c.Fragment = nil, "", ""
	return c.String()
}

// getSpanName returns the method, or "HTTP" for a non-standard one.
func getSpanName(method httpconv.RequestMethodAttr) string {
	if method == httpconv.RequestMethodOther {
		return "HTTP"
	}
	return string(method)
}
