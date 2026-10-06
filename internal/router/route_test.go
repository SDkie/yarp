package router

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/SDkie/yarp/internal/config"
)

// TestMatchHost checks exact, any-host and one-label wildcard host matching.
func TestMatchHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		routeHost string
		host      string
		want      bool
	}{
		{"any host", "", "example.com", true},
		{"exact host", "example.com", "example.com", true},
		{"other host", "example.com", "example.org", false},
		{"wildcard, one label", "*.example.com", "foo.example.com", true},
		{"wildcard, bare domain", "*.example.com", "example.com", false},
		{"wildcard, two labels", "*.example.com", "a.b.example.com", false},
		{"wildcard, empty label", "*.example.com", ".example.com", false},
		{"wildcard, suffix without dot", "*.example.com", "evilexample.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (route{host: tt.routeHost}).matchHost(tt.host); got != tt.want {
				t.Errorf("route %q matchHost(%q) = %v, want %v", tt.routeHost, tt.host, got, tt.want)
			}
		})
	}
}

// TestMatchPath checks that a pathPrefix matches whole path segments only.
func TestMatchPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		pathPrefix string
		path       string
		want       bool
	}{
		{"root matches any path", "/", "/anything/at/all", true},
		{"exact path", "/api", "/api", true},
		{"path under prefix", "/api", "/api/users", true},
		{"trailing slash", "/api", "/api/", true},
		{"longer segment", "/api", "/apix", false},
		{"parent path", "/api", "/", false},
		{"other path", "/api", "/web", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (route{pathPrefix: tt.pathPrefix}).matchPath(tt.path); got != tt.want {
				t.Errorf("route %q matchPath(%q) = %v, want %v", tt.pathPrefix, tt.path, got, tt.want)
			}
		})
	}
}

// TestRequestHost checks that the request host is lowercased and loses its port and brackets.
func TestRequestHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		host string
		want string
	}{
		{"lowercased", "Example.COM", "example.com"},
		{"port removed", "example.com:8080", "example.com"},
		{"IPv4 with port", "127.0.0.1:80", "127.0.0.1"},
		{"IPv6 with port", "[::1]:8080", "::1"},
		{"IPv6 without port", "[::1]", "::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = tt.host
			if got := requestHost(r); got != tt.want {
				t.Errorf("requestHost with Host %q = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// TestCompareSpecificity checks that routes sort most specific first.
func TestCompareSpecificity(t *testing.T) {
	t.Parallel()
	routes := []route{
		{name: "any-root", pathPrefix: "/"},
		{name: "wildcard-root", host: "*.example.com", pathPrefix: "/"},
		{name: "exact-root", host: "api.example.com", pathPrefix: "/"},
		{name: "any-api", pathPrefix: "/api"},
		{name: "exact-api", host: "api.example.com", pathPrefix: "/api"},
		{name: "any-api-v1", pathPrefix: "/api/v1"},
		{name: "b-tie", host: "b.com", pathPrefix: "/x"},
		{name: "a-tie", host: "a.com", pathPrefix: "/x"},
	}
	// Exact host, then wildcard, then any host; then the longer prefix;
	// then by name.
	want := []string{"exact-api", "a-tie", "b-tie", "exact-root", "wildcard-root", "any-api-v1", "any-api", "any-root"}

	slices.SortFunc(routes, compareSpecificity)
	if got := routeNames(routes); !slices.Equal(got, want) {
		t.Errorf("sorted routes = %v, want %v", got, want)
	}
}

// TestNewRoute checks that a route keeps its name, lowercases its host and defaults pathPrefix to "/".
func TestNewRoute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		cfg            config.Route
		wantHost       string
		wantPathPrefix string
	}{
		{"host lowercased", config.Route{Host: "API.Example.com", PathPrefix: "/api"}, "api.example.com", "/api"},
		{"wildcard host lowercased", config.Route{Host: "*.Example.com", PathPrefix: "/api"}, "*.example.com", "/api"},
		{"no pathPrefix becomes root", config.Route{Host: "example.com"}, "example.com", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.Servers = []config.Server{{URL: "http://127.0.0.1:1"}} // never called
			rt := newRoute("r1", cfg, http.DefaultTransport, nil, nil)
			if rt.name != "r1" || rt.host != tt.wantHost || rt.pathPrefix != tt.wantPathPrefix {
				t.Errorf("newRoute = {name %q, host %q, pathPrefix %q}, want {%q, %q, %q}",
					rt.name, rt.host, rt.pathPrefix, "r1", tt.wantHost, tt.wantPathPrefix)
			}
		})
	}
}
