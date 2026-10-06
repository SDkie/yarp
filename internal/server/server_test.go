package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SDkie/yarp/internal/config"
)

// waitTimeout bounds every wait, so a broken server fails the test instead
// of hanging it.
const waitTimeout = 5 * time.Second

var client = &http.Client{Timeout: waitTimeout}

// newBackend starts a backend server that runs h, and returns its URL.
func newBackend(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	backend := httptest.NewServer(h)
	t.Cleanup(backend.Close)
	return backend.URL
}

// echoPath answers with "backend " and the request path.
func echoPath(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "backend "+r.URL.Path)
}

// newServer returns a Server with a "hello" route for /hello on the "web"
// entry point, forwarding to backendURL.
func newServer(t *testing.T, eps map[string]config.EntryPoint, backendURL string) *Server {
	t.Helper()
	routes := map[string]config.Route{
		"hello": {
			PathPrefix:  "/hello",
			EntryPoints: []string{"web"},
			Servers:     []config.Server{{URL: backendURL}},
		},
	}
	s, err := New(eps, routes, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// serving is a Server running Serve in the background.
type serving struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when Serve returns
	err    error         // Serve's result, set before done is closed
}

// start listens and serves s until the test ends.
func start(t *testing.T, s *Server) *serving {
	t.Helper()
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sv := &serving{cancel: cancel, done: make(chan struct{})}
	go func() {
		sv.err = s.Serve(ctx)
		close(sv.done)
	}()
	t.Cleanup(func() {
		sv.cancel()
		sv.wait(t)
	})
	return sv
}

// wait returns Serve's result once it returns.
func (sv *serving) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-sv.done:
		return sv.err
	case <-time.After(waitTimeout):
		t.Fatal("Serve did not return")
		return nil
	}
}

// stop cancels Serve and returns its result.
func (sv *serving) stop(t *testing.T) error {
	t.Helper()
	sv.cancel()
	return sv.wait(t)
}

func get(t *testing.T, url string) (status int, body string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return resp.StatusCode, string(b)
}

// isListening reports whether addr accepts TCP connections.
func isListening(addr string) bool {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// freeAddr returns a loopback address with a port that is free right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestServeProxiesRequests checks that an entry point proxies its routes and others answer 404.
func TestServeProxiesRequests(t *testing.T) {
	t.Parallel()
	s := newServer(t, map[string]config.EntryPoint{
		"web":   {Address: "127.0.0.1:0"},
		"other": {Address: "127.0.0.1:0"},
	}, newBackend(t, echoPath))
	start(t, s)

	if status, body := get(t, "http://"+s.Addr("web")+"/hello/x"); status != http.StatusOK || body != "backend /hello/x" {
		t.Errorf("web: got %d %q, want 200 %q", status, body, "backend /hello/x")
	}
	// The route is only on "web".
	if status, _ := get(t, "http://"+s.Addr("other")+"/hello/x"); status != http.StatusNotFound {
		t.Errorf("other: got %d, want 404", status)
	}
}

// TestAddr checks the address an entry point listens on, before and after Listen.
func TestAddr(t *testing.T) {
	t.Parallel()
	s := newServer(t, map[string]config.EntryPoint{"web": {Address: "127.0.0.1:0"}}, newBackend(t, echoPath))

	if got := s.Addr("web"); got != "" {
		t.Errorf("Addr before Listen = %q, want empty", got)
	}
	start(t, s)

	host, port, err := net.SplitHostPort(s.Addr("web"))
	if err != nil {
		t.Fatalf("Addr = %q: %v", s.Addr("web"), err)
	}
	if host != "127.0.0.1" || port == "0" {
		t.Errorf("Addr = %q, want 127.0.0.1 with the port picked by the system", s.Addr("web"))
	}
	if got := s.Addr("missing"); got != "" {
		t.Errorf("Addr of unknown entry point = %q, want empty", got)
	}
}

// TestListenAddressInUse checks that a taken address fails Listen and releases the others.
func TestListenAddressInUse(t *testing.T) {
	t.Parallel()
	// Listen opens a, b and c in that order. bListener already holds b's
	// address, so a is opened, b fails and c is never tried.
	aAddr := freeAddr(t)
	bListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bListener.Close()
	s := newServer(t, map[string]config.EntryPoint{
		"a": {Address: aAddr},
		"b": {Address: bListener.Addr().String()},
		"c": {Address: "127.0.0.1:0"},
	}, newBackend(t, echoPath))

	err = s.Listen()
	if err == nil || !strings.Contains(err.Error(), `entrypoint "b"`) {
		t.Fatalf("Listen error = %v, want one for entrypoint \"b\"", err)
	}
	if isListening(aAddr) {
		t.Errorf("entry point a still listens on %s after Listen failed", aAddr)
	}
	for _, name := range []string{"a", "c"} {
		if got := s.Addr(name); got != "" {
			t.Errorf("Addr(%s) = %q, want empty", name, got)
		}
	}
	if err := s.Serve(context.Background()); err == nil {
		t.Error("Serve after a failed Listen returned nil, want an error")
	}
}

// TestServeBeforeListen checks that Serve fails when Listen was not called.
func TestServeBeforeListen(t *testing.T) {
	t.Parallel()
	s := newServer(t, map[string]config.EntryPoint{"web": {Address: "127.0.0.1:0"}}, newBackend(t, echoPath))

	err := s.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "before Listen") {
		t.Errorf("Serve error = %v, want one about calling Listen first", err)
	}
}

// TestServeStopsOnCancel checks that cancelling the context stops Serve and closes the port.
func TestServeStopsOnCancel(t *testing.T) {
	t.Parallel()
	s := newServer(t, map[string]config.EntryPoint{"web": {Address: "127.0.0.1:0"}}, newBackend(t, echoPath))
	sv := start(t, s)
	addr := s.Addr("web")
	if status, _ := get(t, "http://"+addr+"/hello"); status != http.StatusOK {
		t.Fatalf("got %d, want 200", status)
	}

	if err := sv.stop(t); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	if isListening(addr) {
		t.Errorf("%s still listens after Serve returned", addr)
	}
}

// TestEntryPointFailureStopsOthers checks that one failing entry point shuts the others down.
func TestEntryPointFailureStopsOthers(t *testing.T) {
	t.Parallel()
	s := newServer(t, map[string]config.EntryPoint{
		"web":   {Address: "127.0.0.1:0"},
		"other": {Address: "127.0.0.1:0"},
	}, newBackend(t, echoPath))
	sv := start(t, s)
	webAddr := s.Addr("web")

	// Nothing in the API makes an entry point fail, so break its listener.
	for _, ep := range s.entryPoints {
		if ep.name == "other" {
			ep.listener.Close()
		}
	}

	err := sv.wait(t)
	if err == nil || !strings.Contains(err.Error(), `entrypoint "other"`) {
		t.Errorf("Serve error = %v, want one for entrypoint \"other\"", err)
	}
	if isListening(webAddr) {
		t.Errorf("web still listens on %s after other failed", webAddr)
	}
}

// TestShutdownWaitsForInFlightRequest checks that shutdown waits for a request in progress.
func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	t.Parallel()
	arrived := make(chan struct{})
	notifyArrived := sync.OnceFunc(func() { close(arrived) })
	release := make(chan struct{})
	releaseBackend := sync.OnceFunc(func() { close(release) })
	s := newServer(t, map[string]config.EntryPoint{"web": {Address: "127.0.0.1:0"}},
		newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			notifyArrived()
			<-release
			io.WriteString(w, "done")
		}))
	sv := start(t, s)
	// Registered last so it runs first: a failed test must not leave the
	// backend blocked, or the cleanups waiting for it would hang.
	t.Cleanup(releaseBackend)
	addr := s.Addr("web")

	type result struct {
		status int
		body   string
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := client.Get("http://" + addr + "/hello")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{status: resp.StatusCode, body: string(b), err: err}
	}()
	select {
	case <-arrived:
	case <-time.After(waitTimeout):
		t.Fatal("request did not reach the backend")
	}

	sv.cancel()
	// Shutdown closes the listener first, then waits for the request.
	deadline := time.Now().Add(waitTimeout)
	for isListening(addr) {
		if time.Now().After(deadline) {
			t.Fatal("still listening after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-sv.done:
		t.Fatal("Serve returned while a request was in flight")
	default:
	}

	releaseBackend()
	res := <-resCh
	if res.err != nil || res.status != http.StatusOK || res.body != "done" {
		t.Errorf("in-flight request: got %d %q %v, want 200 %q", res.status, res.body, res.err, "done")
	}
	if err := sv.wait(t); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
}

// TestNewRouteConflict checks that two routes with the same host and pathPrefix fail New.
func TestNewRouteConflict(t *testing.T) {
	t.Parallel()
	backendURL := newBackend(t, echoPath)
	route := config.Route{
		PathPrefix:  "/hello",
		EntryPoints: []string{"web"},
		Servers:     []config.Server{{URL: backendURL}},
	}
	_, err := New(map[string]config.EntryPoint{"web": {Address: "127.0.0.1:0"}},
		map[string]config.Route{"one": route, "two": route}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "same host and pathPrefix") {
		t.Errorf("New error = %v, want a route conflict", err)
	}
}
