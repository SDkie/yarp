package integration

import (
	"fmt"
	"net/http"
	"testing"
)

// TestRouting checks that each request reaches its most specific matching route.
func TestRouting(t *testing.T) {
	t.Parallel()
	routes := fmt.Sprintf(`
routes:
  api:
    pathPrefix: /api
    entryPoints: [web]
    servers: [{url: %s}]
  api-v2:
    pathPrefix: /api/v2
    entryPoints: [web]
    servers: [{url: %s}]
  example:
    host: example.com
    entryPoints: [web]
    servers: [{url: %s}]
  example-api:
    host: example.com
    pathPrefix: /api
    entryPoints: [web]
    servers: [{url: %s}]
  wildcard:
    host: "*.example.com"
    entryPoints: [web]
    servers: [{url: %s}]
`,
		newNamedBackend(t, "api"),
		newNamedBackend(t, "api-v2"),
		newNamedBackend(t, "example"),
		newNamedBackend(t, "example-api"),
		newNamedBackend(t, "wildcard"),
	)
	y := startYarp(t, webConfig, routes)

	tests := []struct {
		name       string
		host       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"path prefix", "", "/api/users", http.StatusOK, "api"},
		{"path prefix exact", "", "/api", http.StatusOK, "api"},
		{"longer path prefix wins", "", "/api/v2/users", http.StatusOK, "api-v2"},
		{"path prefix matches whole segments", "", "/apix", http.StatusNotFound, "no matching route\n"},
		{"host", "example.com", "/", http.StatusOK, "example"},
		{"host with port and upper case", "EXAMPLE.com:8080", "/", http.StatusOK, "example"},
		{"host and path prefix win over host", "example.com", "/api/users", http.StatusOK, "example-api"},
		{"exact host wins over path prefix", "example.com", "/api/v2", http.StatusOK, "example-api"},
		{"wildcard host", "foo.example.com", "/", http.StatusOK, "wildcard"},
		{"exact host wins over wildcard", "example.com", "/other", http.StatusOK, "example"},
		{"wildcard matches one label", "a.b.example.com", "/", http.StatusNotFound, "no matching route\n"},
		{"no matching route", "other.com", "/other", http.StatusNotFound, "no matching route\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := get(t, y.url("web", tt.path), tt.host)
			if status != tt.wantStatus || body != tt.wantBody {
				t.Errorf("GET %s (Host %q) = %d %q, want %d %q", tt.path, tt.host, status, body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}

// TestEntryPointRoutes checks that an entry point serves only the routes listed on it.
func TestEntryPointRoutes(t *testing.T) {
	t.Parallel()
	routes := fmt.Sprintf(`
routes:
  public:
    pathPrefix: /public
    entryPoints: [web]
    servers: [{url: %s}]
  private:
    pathPrefix: /private
    entryPoints: [admin]
    servers: [{url: %s}]
  shared:
    pathPrefix: /shared
    entryPoints: [web, admin]
    servers: [{url: %s}]
`,
		newNamedBackend(t, "public"),
		newNamedBackend(t, "private"),
		newNamedBackend(t, "shared"),
	)
	y := startYarp(t, webAdminConfig, routes)

	tests := []struct {
		entryPoint string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"web", "/public", http.StatusOK, "public"},
		{"web", "/private", http.StatusNotFound, "no matching route\n"},
		{"web", "/shared", http.StatusOK, "shared"},
		{"admin", "/public", http.StatusNotFound, "no matching route\n"},
		{"admin", "/private", http.StatusOK, "private"},
		{"admin", "/shared", http.StatusOK, "shared"},
	}
	for _, tt := range tests {
		t.Run(tt.entryPoint+tt.path, func(t *testing.T) {
			status, body := get(t, y.url(tt.entryPoint, tt.path), "")
			if status != tt.wantStatus || body != tt.wantBody {
				t.Errorf("GET %s = %d %q, want %d %q", tt.path, status, body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}

// TestRoundRobin checks that a route sends requests to its servers in turn.
func TestRoundRobin(t *testing.T) {
	t.Parallel()
	routes := fmt.Sprintf(`
routes:
  app:
    pathPrefix: /
    entryPoints: [web]
    servers: [{url: %s}, {url: %s}, {url: %s}]
`,
		newNamedBackend(t, "a"),
		newNamedBackend(t, "b"),
		newNamedBackend(t, "c"),
	)
	y := startYarp(t, webConfig, routes)

	for i, want := range []string{"a", "b", "c", "a", "b", "c"} {
		if _, body := get(t, y.url("web", "/"), ""); body != want {
			t.Errorf("request %d went to %q, want %q", i, body, want)
		}
	}
}

// TestRoundRobinSharedAcrossEntryPoints checks that a route on two entry
// points has one round-robin, not one per entry point.
func TestRoundRobinSharedAcrossEntryPoints(t *testing.T) {
	t.Parallel()
	routes := fmt.Sprintf(`
routes:
  app:
    pathPrefix: /
    entryPoints: [web, admin]
    servers: [{url: %s}, {url: %s}]
`,
		newNamedBackend(t, "a"),
		newNamedBackend(t, "b"),
	)
	y := startYarp(t, webAdminConfig, routes)

	requests := []struct {
		entryPoint string
		want       string
	}{
		{"web", "a"},
		{"admin", "b"},
		{"web", "a"},
		{"admin", "b"},
	}
	for i, r := range requests {
		if _, body := get(t, y.url(r.entryPoint, "/"), ""); body != r.want {
			t.Errorf("request %d on %s went to %q, want %q", i, r.entryPoint, body, r.want)
		}
	}
}
