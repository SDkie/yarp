// Package router matches incoming requests to the configured routes.
package router

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SDkie/yarp/internal/config"
)

// Router is the http.Handler for one entry point. It sends each request to
// the most specific matching route, or answers 404 when none matches.
type Router struct {
	entryPoint string
	// routes is sorted most specific first, so the first match wins.
	routes []route
}

type route struct {
	name string
	// host is "" (any host), an exact host, or a "*.example.com" wildcard.
	host string
	// pathPrefix is never empty; a route without one uses "/".
	pathPrefix string
	handler    http.Handler
}

// Build returns one handler per entry point, serving the routes that list
// that entry point. Entry points without routes answer 404 to every request.
// It fails if two routes on the same entry point have the same host and
// pathPrefix, since only one of them could ever match. All routes share one
// backend transport, so they reuse the same connection pool.
func Build(entryPoints map[string]config.EntryPoint, routes map[string]config.Route) (map[string]http.Handler, error) {
	transport := newTransport()
	byEntryPoint := make(map[string][]route, len(entryPoints))
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		r := routes[name]
		proxy, err := newProxy(name, r.Servers, transport)
		if err != nil {
			return nil, err
		}
		rt := route{
			name:       name,
			host:       normalizeHost(r.Host),
			pathPrefix: cmp.Or(r.PathPrefix, "/"),
			handler:    proxy,
		}
		for _, ep := range r.EntryPoints {
			byEntryPoint[ep] = append(byEntryPoint[ep], rt)
		}
	}

	handlers := make(map[string]http.Handler, len(entryPoints))
	for ep := range entryPoints {
		rs := byEntryPoint[ep]
		slices.SortFunc(rs, compareSpecificity)
		for i := 1; i < len(rs); i++ {
			if rs[i-1].host == rs[i].host && rs[i-1].pathPrefix == rs[i].pathPrefix {
				return nil, fmt.Errorf("entry point %q: routes %q and %q have the same host and pathPrefix",
					ep, rs[i-1].name, rs[i].name)
			}
		}
		handlers[ep] = &Router{entryPoint: ep, routes: rs}
	}
	return handlers, nil
}

// compareSpecificity orders routes most specific first: exact host, then
// wildcard host, then no host; then the longer pathPrefix; then by name.
func compareSpecificity(a, b route) int {
	return cmp.Or(
		cmp.Compare(hostRank(a.host), hostRank(b.host)),
		cmp.Compare(len(b.pathPrefix), len(a.pathPrefix)),
		cmp.Compare(a.name, b.name),
	)
}

func hostRank(host string) int {
	switch {
	case host == "":
		return 2
	case strings.HasPrefix(host, "*."):
		return 1
	default:
		return 0
	}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	routeName := ""

	host := requestHost(r)
	if i := slices.IndexFunc(rt.routes, func(route route) bool {
		return route.matchHost(host) && route.matchPath(r.URL.Path)
	}); i >= 0 {
		routeName = rt.routes[i].name
		rt.routes[i].handler.ServeHTTP(w, r)
	} else {
		http.Error(w, "no matching route", http.StatusNotFound)
	}

	slog.Info("request",
		"entrypoint", rt.entryPoint,
		"route", routeName,
		"method", r.Method,
		"host", r.Host,
		"path", r.URL.Path,
		"duration", time.Since(start),
		"remote", r.RemoteAddr,
	)
}

// matchHost reports whether host (lowercase, without port) matches. A
// wildcard matches exactly one extra label: "*.example.com" matches
// "foo.example.com" but not "example.com" or "a.b.example.com".
func (r route) matchHost(host string) bool {
	if r.host == "" || r.host == host {
		return true
	}
	suffix, ok := strings.CutPrefix(r.host, "*")
	if !ok {
		return false
	}
	label, ok := strings.CutSuffix(host, suffix)
	return ok && label != "" && !strings.Contains(label, ".")
}

// matchPath reports whether path is pathPrefix or lies under it, matching
// whole segments: "/api" matches "/api" and "/api/users" but not "/apix".
func (r route) matchPath(path string) bool {
	if r.pathPrefix == "/" || path == r.pathPrefix {
		return true
	}
	return strings.HasPrefix(path, r.pathPrefix+"/")
}

// requestHost returns the request's host in lowercase, without port or
// IPv6 brackets.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return normalizeHost(host)
}

func normalizeHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(host)
}
