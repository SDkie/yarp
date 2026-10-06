package router

import (
	"cmp"
	"net"
	"net/http"
	"strings"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/httpcache"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
)

// route is one routing rule: requests whose host and path match go to its
// handler.
type route struct {
	name string
	// host is "" (any host), an exact host, or a "*.example.com" wildcard.
	host string
	// pathPrefix is never empty; a route without one uses "/".
	pathPrefix string
	handler    http.Handler
}

// newRoute builds the route name and its handler chain: a proxy to its
// servers, then the cache when cache is not nil.
func newRoute(name string, cfg config.Route, transport http.RoundTripper, cache *cache.Cache, tel *telemetry.Telemetry) route {
	handler := newProxy(name, cfg.Servers, tel.Transport(transport, name))
	if cache != nil {
		handler = httpcache.New(cache, tel, handler)
	}
	return route{
		name:       name,
		host:       normalizeHost(cfg.Host),
		pathPrefix: cmp.Or(cfg.PathPrefix, "/"),
		handler:    handler,
	}
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
