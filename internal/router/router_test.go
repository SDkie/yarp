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

func routeNames(routes []Route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.Name
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

// TestNew checks that routes are sorted most specific first and duplicates rejected.
func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		routes    []Route
		wantOrder []string
		wantErr   bool
	}{
		{
			name: "sorted most specific first",
			routes: []Route{
				{Name: "any"},
				{Name: "exact", Host: "a.com"},
				{Name: "wildcard", Host: "*.a.com"},
			},
			wantOrder: []string{"exact", "wildcard", "any"},
		},
		{
			name: "same pathPrefix, different hosts",
			routes: []Route{
				{Name: "a", Host: "a.com", PathPrefix: "/x"},
				{Name: "b", Host: "b.com", PathPrefix: "/x"},
			},
			wantOrder: []string{"a", "b"},
		},
		{
			name: "same host and pathPrefix",
			routes: []Route{
				{Name: "a", Host: "a.com", PathPrefix: "/x"},
				{Name: "b", Host: "a.com", PathPrefix: "/x"},
			},
			wantErr: true,
		},
		{
			name: "same host in other case",
			routes: []Route{
				{Name: "a", Host: "X.com", PathPrefix: "/x"},
				{Name: "b", Host: "x.com", PathPrefix: "/x"},
			},
			wantErr: true,
		},
		{
			name: "empty and root pathPrefix are the same",
			routes: []Route{
				{Name: "a", Host: "a.com"},
				{Name: "b", Host: "a.com", PathPrefix: "/"},
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
			for i := range tt.routes {
				tt.routes[i].Handler = named(tt.routes[i].Name)
			}
			rt, err := New("web", tt.routes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New error = %v, wantErr %v", err, tt.wantErr)
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

// TestNewNilHandler checks that a route without a Handler is rejected, naming the route.
func TestNewNilHandler(t *testing.T) {
	t.Parallel()
	_, err := New("web", []Route{{Name: "ok", Handler: named("ok")}, {Name: "bad", PathPrefix: "/x"}})
	if want := `route "bad" has no handler`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("New error = %v, want %q", err, want)
	}
}

// TestNewNormalizes checks that New lowercases hosts, drops IPv6 brackets and defaults pathPrefix to "/".
func TestNewNormalizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		route          Route
		wantHost       string
		wantPathPrefix string
	}{
		{"host lowercased", Route{Host: "API.Example.com", PathPrefix: "/api"}, "api.example.com", "/api"},
		{"wildcard host lowercased", Route{Host: "*.Example.com", PathPrefix: "/api"}, "*.example.com", "/api"},
		{"IPv6 brackets removed", Route{Host: "[::1]", PathPrefix: "/api"}, "::1", "/api"},
		{"no pathPrefix becomes root", Route{Host: "example.com"}, "example.com", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.route
			in.Name, in.Handler = "r1", named("h")
			rt, err := New("web", []Route{in})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got := rt.routes[0]
			if got.Name != "r1" || got.Host != tt.wantHost || got.PathPrefix != tt.wantPathPrefix {
				t.Errorf("route = {Name %q, Host %q, PathPrefix %q}, want {%q, %q, %q}",
					got.Name, got.Host, got.PathPrefix, "r1", tt.wantHost, tt.wantPathPrefix)
			}
			if body := serve(got.Handler, "http://example.com/").Body.String(); body != "h" {
				t.Errorf("route handler answered %q, want %q", body, "h")
			}
		})
	}
}

// TestServeHTTP checks that each request reaches its most specific route, or gets 404.
func TestServeHTTP(t *testing.T) {
	t.Parallel()
	rt, err := New("web", []Route{
		{Name: "api", Host: "api.example.com", PathPrefix: "/api", Handler: named("api")},
		{Name: "any-api", PathPrefix: "/api", Handler: named("any-api")},
		{Name: "static", PathPrefix: "/static", Handler: named("static")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
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
