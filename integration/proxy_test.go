package integration

import (
	"fmt"
	"io"
	"net/http"
	"testing"
)

// appRoutes returns a routes file with one route, "app", for every request
// on "web", forwarding to serverURL.
func appRoutes(serverURL string) string {
	return fmt.Sprintf(`
routes:
  app:
    pathPrefix: /
    entryPoints: [web]
    servers: [{url: %q}]
`, serverURL)
}

// TestProxyForwardsRequest checks the method, path, query, Host and body the backend receives.
func TestProxyForwardsRequest(t *testing.T) {
	t.Parallel()
	backendURL, reqs := newCaptureBackend(t)
	y := startYarp(t, webConfig, appRoutes(backendURL+"/base?k=v"))

	req := newRequest(t, http.MethodPost, y.url("web", "/items/1?q=2"), "hello")
	req.Host = "app.test"
	resp, _ := do(t, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	got := receive(t, reqs)
	if got.req.Method != http.MethodPost {
		t.Errorf("method = %q, want %q", got.req.Method, http.MethodPost)
	}
	if got.req.URL.Path != "/base/items/1" {
		t.Errorf("path = %q, want %q", got.req.URL.Path, "/base/items/1")
	}
	if got.req.URL.RawQuery != "k=v&q=2" {
		t.Errorf("query = %q, want %q", got.req.URL.RawQuery, "k=v&q=2")
	}
	if got.req.Host != "app.test" {
		t.Errorf("Host = %q, want %q", got.req.Host, "app.test")
	}
	if got.body != "hello" {
		t.Errorf("body = %q, want %q", got.body, "hello")
	}
}

// TestProxySetsForwardedHeaders checks that the X-Forwarded-* headers describe
// the client, replacing any the client sent.
func TestProxySetsForwardedHeaders(t *testing.T) {
	t.Parallel()
	backendURL, reqs := newCaptureBackend(t)
	y := startYarp(t, webConfig, appRoutes(backendURL))

	req := newRequest(t, http.MethodGet, y.url("web", "/"), "")
	req.Host = "app.test"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	req.Header.Set("X-Forwarded-Host", "spoofed.test")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=203.0.113.1")
	do(t, req)

	h := receive(t, reqs).req.Header
	want := map[string]string{
		"X-Forwarded-For":   "127.0.0.1",
		"X-Forwarded-Host":  "app.test",
		"X-Forwarded-Proto": "http",
		"Forwarded":         "",
	}
	for name, v := range want {
		if got := h.Values(name); len(got) > 1 || h.Get(name) != v {
			t.Errorf("%s = %q, want %q", name, got, v)
		}
	}
}

// TestProxyRemovesHopByHopHeaders checks that hop-by-hop headers are dropped
// both ways, including those named in Connection.
func TestProxyRemovesHopByHopHeaders(t *testing.T) {
	t.Parallel()
	reqs := make(chan received, 1)
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		reqs <- received{req: r}
		w.Header().Set("Connection", "X-Backend-Hop")
		w.Header().Set("X-Backend-Hop", "1")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Backend-End", "1")
	})
	y := startYarp(t, webConfig, appRoutes(backendURL))

	req := newRequest(t, http.MethodGet, y.url("web", "/"), "")
	req.Header.Set("Connection", "X-Client-Hop")
	req.Header.Set("X-Client-Hop", "1")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	req.Header.Set("X-Client-End", "1")
	resp, _ := do(t, req)

	reqHeader := receive(t, reqs).req.Header
	for _, name := range []string{"X-Client-Hop", "Keep-Alive", "Proxy-Authorization"} {
		if v := reqHeader.Get(name); v != "" {
			t.Errorf("backend got %s = %q, want none", name, v)
		}
	}
	if v := reqHeader.Get("X-Client-End"); v != "1" {
		t.Errorf("backend got X-Client-End = %q, want %q", v, "1")
	}
	for _, name := range []string{"X-Backend-Hop", "Keep-Alive"} {
		if v := resp.Header.Get(name); v != "" {
			t.Errorf("client got %s = %q, want none", name, v)
		}
	}
	if v := resp.Header.Get("X-Backend-End"); v != "1" {
		t.Errorf("client got X-Backend-End = %q, want %q", v, "1")
	}
}

// TestProxyResponse checks that the client gets the backend's status, headers and body.
func TestProxyResponse(t *testing.T) {
	t.Parallel()
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "created")
	})
	y := startYarp(t, webConfig, appRoutes(backendURL))

	resp, body := do(t, newRequest(t, http.MethodPost, y.url("web", "/"), ""))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if v := resp.Header.Get("X-Backend"); v != "yes" {
		t.Errorf("X-Backend = %q, want %q", v, "yes")
	}
	if body != "created" {
		t.Errorf("body = %q, want %q", body, "created")
	}
}

// TestProxyTrailers checks that the backend's trailers reach the client.
func TestProxyTrailers(t *testing.T) {
	t.Parallel()
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Checksum")
		io.WriteString(w, "body")
		w.Header().Set("X-Checksum", "abc")
	})
	y := startYarp(t, webConfig, appRoutes(backendURL))

	resp, body := do(t, newRequest(t, http.MethodGet, y.url("web", "/"), ""))
	if body != "body" {
		t.Errorf("body = %q, want %q", body, "body")
	}
	if v := resp.Trailer.Get("X-Checksum"); v != "abc" {
		t.Errorf("trailer X-Checksum = %q, want %q", v, "abc")
	}
}

// TestProxyPassesRedirects checks that a backend redirect is sent to the
// client, not followed by yarp.
func TestProxyPassesRedirects(t *testing.T) {
	t.Parallel()
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	y := startYarp(t, webConfig, appRoutes(backendURL))

	resp, _ := do(t, newRequest(t, http.MethodGet, y.url("web", "/"), ""))
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if v := resp.Header.Get("Location"); v != "/elsewhere" {
		t.Errorf("Location = %q, want %q", v, "/elsewhere")
	}
}

// TestBackendUnreachable checks that a backend nothing listens on gets 502.
func TestBackendUnreachable(t *testing.T) {
	t.Parallel()
	y := startYarp(t, webConfig, appRoutes(closedURL(t)))

	if status, _ := get(t, y.url("web", "/"), ""); status != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", status, http.StatusBadGateway)
	}
}

// TestBackendBodyInterrupted checks that a backend dying mid-body breaks the
// client's response instead of ending it as if it were whole. The body is
// chunked, as a short Content-Length body would break the client anyway.
func TestBackendBodyInterrupted(t *testing.T) {
	t.Parallel()
	backendURL := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "partial")
		http.NewResponseController(w).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("backend: Hijack: %v", err)
			return
		}
		conn.Close()
	})
	y := startYarp(t, webConfig, appRoutes(backendURL))

	resp, err := client.Get(y.url("web", "/"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("reading the body succeeded, want an error")
	}
}
