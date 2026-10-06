// Package router matches incoming requests to the configured routes.
package router

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
)

// Router is the http.Handler for one entry point. It sends each request to
// the most specific matching route, or answers 404 when none matches.
type Router struct {
	entryPoint string
	// routes is sorted most specific first, so the first match wins.
	routes []route
}

// Build returns one handler per entry point, serving the routes that list
// that entry point. Entry points without routes answer 404 to every request.
// It fails if two routes on the same entry point have the same host and
// pathPrefix, since only one of them could ever match. All routes share one
// backend transport, so they reuse the same connection pool. Responses are
// cached in cache; a nil cache disables caching. Requests are traced and measured
// with tel; a nil tel disables that.
func Build(entryPoints map[string]config.EntryPoint, routes map[string]config.Route, cache *cache.Cache, tel *telemetry.Telemetry) (map[string]http.Handler, error) {
	transport := newTransport()
	byEntryPoint := make(map[string][]route, len(entryPoints))
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		cfg := routes[name]
		rt := newRoute(name, cfg, transport, cache, tel)
		for _, ep := range cfg.EntryPoints {
			byEntryPoint[ep] = append(byEntryPoint[ep], rt)
		}
	}

	handlers := make(map[string]http.Handler, len(entryPoints))
	for ep := range entryPoints {
		router, err := newRouter(ep, byEntryPoint[ep])
		if err != nil {
			return nil, err
		}
		handlers[ep] = tel.Handler(ep, router)
	}
	return handlers, nil
}

// newRouter returns the router for entryPoint with routes sorted most
// specific first. It fails if two routes have the same host and pathPrefix.
func newRouter(entryPoint string, routes []route) (*Router, error) {
	if len(routes) == 0 {
		slog.Error("entry point has no routes, every request gets 404", "entrypoint", entryPoint)
	}
	slices.SortFunc(routes, compareSpecificity)
	for i := 1; i < len(routes); i++ {
		if routes[i-1].host == routes[i].host && routes[i-1].pathPrefix == routes[i].pathPrefix {
			return nil, fmt.Errorf("entry point %q: routes %q and %q have the same host and pathPrefix",
				entryPoint, routes[i-1].name, routes[i].name)
		}
	}
	return &Router{entryPoint: entryPoint, routes: routes}, nil
}

// ServeHTTP sends r to the most specific matching route, or answers 404,
// then writes the access log line.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	matched, ok := rt.match(r)
	if ok {
		telemetry.SetRoute(r.Context(), matched.name)
		matched.handler.ServeHTTP(w, r)
	} else {
		slog.ErrorContext(r.Context(), "no matching route", "entrypoint", rt.entryPoint, "method", r.Method, "host", r.Host, "path", r.URL.Path)
		http.Error(w, "no matching route", http.StatusNotFound)
	}
	rt.logRequest(r, matched.name, start)
}

// match returns the most specific route for r, or false when none matches.
func (rt *Router) match(r *http.Request) (route, bool) {
	host := requestHost(r)
	for _, candidate := range rt.routes {
		if candidate.matchHost(host) && candidate.matchPath(r.URL.Path) {
			return candidate, true
		}
	}
	return route{}, false
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
