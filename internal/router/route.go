package router

import (
	"cmp"
	"net"
	"net/http"
	"strings"
)

// Route sends requests whose host and path match to Handler.
type Route struct {
	// Name identifies the route in logs, errors and telemetry.
	Name string
	// Host is "" (any host), an exact host, or a "*.example.com" wildcard.
	Host string
	// PathPrefix matches whole path segments; "" means "/".
	PathPrefix string
	Handler    http.Handler
}

// compareSpecificity orders routes most specific first: exact host, then
// wildcard host, then no host; then the longer pathPrefix; then by name.
func compareSpecificity(a, b Route) int {
	return cmp.Or(
		cmp.Compare(hostRank(a.Host), hostRank(b.Host)),
		cmp.Compare(len(b.PathPrefix), len(a.PathPrefix)),
		cmp.Compare(a.Name, b.Name),
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
func (r Route) matchHost(host string) bool {
	if r.Host == "" || r.Host == host {
		return true
	}
	suffix, ok := strings.CutPrefix(r.Host, "*")
	if !ok {
		return false
	}
	label, ok := strings.CutSuffix(host, suffix)
	return ok && label != "" && !strings.Contains(label, ".")
}

// matchPath reports whether path is pathPrefix or lies under it, matching
// whole segments: "/api" matches "/api" and "/api/users" but not "/apix".
func (r Route) matchPath(path string) bool {
	if r.PathPrefix == "/" || path == r.PathPrefix {
		return true
	}
	return strings.HasPrefix(path, r.PathPrefix+"/")
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
