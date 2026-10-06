// Package router matches incoming requests to routes.
package router

import (
	"cmp"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/SDkie/yarp/internal/middlewares/telemetry"
)

// Router is the http.Handler for one entry point. It sends each request to
// the most specific matching route, or answers 404 when none matches.
type Router struct {
	entryPoint string
	// routes holds normalized copies (see New), sorted most specific first so
	// the first match wins.
	routes []Route
}

// New returns the router for entryPoint, trying routes most specific first;
// with no routes, every request gets 404. It fails if two routes have the
// same host and pathPrefix, since only one of them could ever match, or if a
// route has no Handler.
func New(entryPoint string, routes []Route) (*Router, error) {
	if len(routes) == 0 {
		slog.Error("entry point has no routes, every request gets 404", "entrypoint", entryPoint)
	}
	// Normalized copies, so matching compares hosts and paths directly.
	rs := make([]Route, len(routes))
	for i, r := range routes {
		if r.Handler == nil {
			return nil, fmt.Errorf("entry point %q: route %q has no handler", entryPoint, r.Name)
		}
		r.Host = normalizeHost(r.Host)
		r.PathPrefix = cmp.Or(r.PathPrefix, "/")
		rs[i] = r
	}
	slices.SortFunc(rs, compareSpecificity)
	for i := 1; i < len(rs); i++ {
		if rs[i-1].Host == rs[i].Host && rs[i-1].PathPrefix == rs[i].PathPrefix {
			return nil, fmt.Errorf("entry point %q: routes %q and %q have the same host and pathPrefix",
				entryPoint, rs[i-1].Name, rs[i].Name)
		}
	}
	return &Router{entryPoint: entryPoint, routes: rs}, nil
}

// ServeHTTP sends r to the most specific matching route, or answers 404,
// then writes the access log line.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	matched, ok := rt.match(r)
	if ok {
		telemetry.SetRoute(r.Context(), matched.Name)
		matched.Handler.ServeHTTP(w, r)
	} else {
		slog.ErrorContext(r.Context(), "no matching route", "entrypoint", rt.entryPoint, "method", r.Method, "host", r.Host, "path", r.URL.Path)
		http.Error(w, "no matching route", http.StatusNotFound)
	}
	rt.logRequest(r, matched.Name, start)
}

// match returns the most specific route for r, or false when none matches.
func (rt *Router) match(r *http.Request) (Route, bool) {
	host := requestHost(r)
	for _, candidate := range rt.routes {
		if candidate.matchHost(host) && candidate.matchPath(r.URL.Path) {
			return candidate, true
		}
	}
	return Route{}, false
}

// logRequest writes the access log line for r, served by routeName since
// start. Its fields are built only when Info is enabled.
func (rt *Router) logRequest(r *http.Request, routeName string, start time.Time) {
	ctx := r.Context()
	if !slog.Default().Enabled(ctx, slog.LevelInfo) {
		return
	}
	slog.InfoContext(ctx, "request completed",
		"entrypoint", rt.entryPoint,
		"route", routeName,
		"method", r.Method,
		"host", r.Host,
		"path", r.URL.Path,
		"duration", time.Since(start),
		"remote", r.RemoteAddr,
	)
}
