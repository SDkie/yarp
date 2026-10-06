package router

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SDkie/yarp/internal/config"
)

// named returns a handler that answers with name, standing in for a route's
// chain.
func named(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name)
	})
}

// serve sends a GET for url to h and returns the recorded response.
func serve(h http.Handler, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func routeNames(routes []route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.name
	}
	return names
}

// setDefaultLogger makes h the default slog handler until the test ends.
func setDefaultLogger(t *testing.T, h slog.Handler) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
}

// TestNewRouter checks that routes are sorted and duplicates rejected.
func TestNewRouter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		routes    []route
		wantOrder []string
		wantErr   bool
	}{
		{
			name: "sorted most specific first",
			routes: []route{
				{name: "any", pathPrefix: "/"},
				{name: "exact", host: "a.com", pathPrefix: "/"},
				{name: "wildcard", host: "*.a.com", pathPrefix: "/"},
			},
			wantOrder: []string{"exact", "wildcard", "any"},
		},
		{
			name: "same pathPrefix, different hosts",
			routes: []route{
				{name: "a", host: "a.com", pathPrefix: "/x"},
				{name: "b", host: "b.com", pathPrefix: "/x"},
			},
			wantOrder: []string{"a", "b"},
		},
		{
			name: "same host and pathPrefix",
			routes: []route{
				{name: "a", host: "a.com", pathPrefix: "/x"},
				{name: "b", host: "a.com", pathPrefix: "/x"},
			},
			wantErr: true,
		},
		{
			name:      "no routes",
			wantOrder: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, err := newRouter("web", tt.routes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("newRouter error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got := routeNames(rt.routes); !slices.Equal(got, tt.wantOrder) {
				t.Errorf("routes = %v, want %v", got, tt.wantOrder)
			}
		})
	}
}

// TestServeHTTP checks that each request reaches its most specific route, or gets 404.
func TestServeHTTP(t *testing.T) {
	t.Parallel()
	rt, err := newRouter("web", []route{
		{name: "api", host: "api.example.com", pathPrefix: "/api", handler: named("api")},
		{name: "any-api", pathPrefix: "/api", handler: named("any-api")},
		{name: "static", pathPrefix: "/static", handler: named("static")},
	})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	tests := []struct {
		name       string
		url        string
		wantStatus int
		wantBody   string
	}{
		{"exact host wins", "http://api.example.com/api/users", http.StatusOK, "api"},
		{"host with port and upper case", "http://API.Example.com:8080/api", http.StatusOK, "api"},
		{"any host as fallback", "http://other.com/api/users", http.StatusOK, "any-api"},
		{"other route", "http://other.com/static/app.js", http.StatusOK, "static"},
		{"no matching route", "http://other.com/missing", http.StatusNotFound, "no matching route\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(rt, tt.url)
			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody {
				t.Errorf("GET %s = %d %q, want %d %q", tt.url, rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

// TestLogRequest checks the access log line; it swaps the default logger, so it is not parallel.
func TestLogRequest(t *testing.T) {
	rt := &Router{entryPoint: "web"}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)

	// At Info, the line holds every field.
	t.Run("info", func(t *testing.T) {
		var buf bytes.Buffer
		setDefaultLogger(t, slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		rt.logRequest(r, "r1", time.Now())
		got := buf.String()
		for _, want := range []string{`msg="request completed"`, "entrypoint=web", "route=r1", "method=GET",
			"host=example.com", "path=/x", "duration=", "remote=" + r.RemoteAddr} {
			if !strings.Contains(got, want) {
				t.Errorf("log line %q does not contain %q", got, want)
			}
		}
	})
	// With Info disabled, nothing is built or written.
	t.Run("info disabled", func(t *testing.T) {
		var buf bytes.Buffer
		setDefaultLogger(t, slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
		allocs := testing.AllocsPerRun(100, func() { rt.logRequest(r, "r1", time.Now()) })
		if allocs != 0 {
			t.Errorf("logRequest allocates %v times with Info disabled, want 0", allocs)
		}
		if buf.Len() != 0 {
			t.Errorf("logRequest wrote %q with Info disabled, want nothing", buf.String())
		}
	})
}

// TestBuild checks that Build returns a handler per entry point and rejects duplicate routes.
func TestBuild(t *testing.T) {
	t.Parallel()
	servers := []config.Server{{URL: "http://127.0.0.1:1"}} // never called
	web, admin := []string{"web"}, []string{"admin"}
	entryPoints := map[string]config.EntryPoint{"web": {}, "admin": {}}
	tests := []struct {
		name    string
		routes  map[string]config.Route
		wantErr bool
	}{
		{
			name: "routes on both entry points",
			routes: map[string]config.Route{
				"a": {Host: "a.com", EntryPoints: web, Servers: servers},
				"b": {Host: "b.com", EntryPoints: admin, Servers: servers},
			},
		},
		{
			name: "entry point without routes",
			routes: map[string]config.Route{
				"a": {Host: "a.com", EntryPoints: web, Servers: servers},
			},
		},
		{
			name: "same host and pathPrefix on one entry point",
			routes: map[string]config.Route{
				"a": {Host: "a.com", PathPrefix: "/p", EntryPoints: web, Servers: servers},
				"b": {Host: "a.com", PathPrefix: "/p", EntryPoints: web, Servers: servers},
			},
			wantErr: true,
		},
		{
			name: "same host in other case",
			routes: map[string]config.Route{
				"a": {Host: "X.com", PathPrefix: "/p", EntryPoints: web, Servers: servers},
				"b": {Host: "x.com", PathPrefix: "/p", EntryPoints: web, Servers: servers},
			},
			wantErr: true,
		},
		{
			name: "same host and pathPrefix on different entry points",
			routes: map[string]config.Route{
				"a": {Host: "a.com", PathPrefix: "/p", EntryPoints: web, Servers: servers},
				"b": {Host: "a.com", PathPrefix: "/p", EntryPoints: admin, Servers: servers},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers, err := Build(entryPoints, tt.routes, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build error = %v, wantErr %v", err, tt.wantErr)
			}
			for ep := range entryPoints {
				if err == nil && handlers[ep] == nil {
					t.Errorf("Build returned no handler for entry point %q", ep)
				}
			}
		})
	}
}
