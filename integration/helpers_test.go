// Package integration tests yarp's packages wired together: a config loaded
// from files, served by a real server in front of real backends.
package integration

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/server"
)

// waitTimeout bounds every wait, so a broken server fails the test instead
// of hanging it.
const waitTimeout = 5 * time.Second

// client does not follow redirects, so tests see what yarp sent.
var client = &http.Client{
	Timeout: waitTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// webConfig is a config file with one entry point, "web", and no cache.
const webConfig = `
entryPoints:
  web:
    address: 127.0.0.1:0
providers:
  file:
    filename: routes.yml
cache:
  enabled: false
`

// webAdminConfig is a config file with two entry points, "web" and
// "admin", and no cache.
const webAdminConfig = `
entryPoints:
  web:
    address: 127.0.0.1:0
  admin:
    address: 127.0.0.1:0
providers:
  file:
    filename: routes.yml
cache:
  enabled: false
`

// yarp is a running yarp server.
type yarp struct {
	srv *server.Server
}

// startYarp writes configYAML and routesYAML (as routes.yml) to a temp dir,
// loads them and serves them until the test ends.
func startYarp(t *testing.T, configYAML, routesYAML string) *yarp {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "yarp.yml")
	writeFile(t, configPath, configYAML)
	writeFile(t, filepath.Join(dir, "routes.yml"), routesYAML)

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, err := server.New(cfg.EntryPoints, cfg.Routes, nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(waitTimeout):
			t.Error("Serve did not return")
		}
	})
	return &yarp{srv: srv}
}

// url returns the URL of path on the named entry point.
func (y *yarp) url(entryPoint, path string) string {
	return "http://" + y.srv.Addr(entryPoint) + path
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newBackend starts a backend server that runs h, and returns its URL.
func newBackend(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	backend := httptest.NewServer(h)
	t.Cleanup(backend.Close)
	return backend.URL
}

// newNamedBackend starts a backend that answers with its name, and returns
// its URL.
func newNamedBackend(t *testing.T, name string) string {
	t.Helper()
	return newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name)
	})
}

// received is a request as a backend received it.
type received struct {
	req  *http.Request
	body string
}

// newCaptureBackend starts a backend that answers 200 and sends each request
// it receives to the returned channel, and returns its URL.
func newCaptureBackend(t *testing.T) (string, <-chan received) {
	t.Helper()
	reqs := make(chan received, 1)
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reqs <- received{req: r, body: string(b)}
	})
	return backendURL, reqs
}

// receive returns the next request from reqs.
func receive(t *testing.T, reqs <-chan received) received {
	t.Helper()
	select {
	case r := <-reqs:
		return r
	case <-time.After(waitTimeout):
		t.Fatal("backend received no request")
		return received{}
	}
}

// closedURL returns the URL of a loopback port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	return "http://" + ln.Addr().String()
}

// do sends req and returns the response with its body read.
func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL, err)
	}
	return resp, string(b)
}

// get sends a GET for rawURL with the given Host header ("" keeps the
// URL's) and returns the status and body.
func get(t *testing.T, rawURL, host string) (status int, body string) {
	t.Helper()
	req := newRequest(t, http.MethodGet, rawURL, "")
	if host != "" {
		req.Host = host
	}
	resp, body := do(t, req)
	return resp.StatusCode, body
}

func newRequest(t *testing.T, method, rawURL, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}
