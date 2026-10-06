package telemetry

import (
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
)

// yarp's own attributes.
const (
	entryPointKey  = attribute.Key("yarp.entrypoint")
	routeKey       = attribute.Key("yarp.route")
	cacheResultKey = attribute.Key("yarp.cache.result")
)

// scheme is the url.scheme of every request received; entry points serve
// plain HTTP only.
const scheme = "http"

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

// getSpanName returns the method, or "HTTP" for a non-standard one.
func getSpanName(method httpconv.RequestMethodAttr) string {
	if method == httpconv.RequestMethodOther {
		return "HTTP"
	}
	return string(method)
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

// getURLWithoutQuery returns u without its query, fragment and user info,
// which may hold secrets.
func getURLWithoutQuery(u *url.URL) string {
	c := *u
	c.User, c.RawQuery, c.Fragment = nil, "", ""
	return c.String()
}
