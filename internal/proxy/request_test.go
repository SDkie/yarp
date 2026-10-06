package proxy

import (
	"crypto/tls"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestOutgoingRequestURL checks the URL, Host and request fields of the backend request.
func TestOutgoingRequestURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		url     string
		wantURL string
	}{
		{"path appended to base path", "http://b:8080/base", "http://x.example/a/b", "http://b:8080/base/a/b"},
		{"queries merged", "http://b/base?k=v", "http://x.example/a?x=1", "http://b/base/a?k=v&x=1"},
		{"target query only", "http://b/?k=v", "http://x.example/a", "http://b/a?k=v"},
		{"request query only", "http://b", "http://x.example/a?x=1", "http://b/a?x=1"},
		{"escaped slash kept", "http://b/base", "http://x.example/a%2Fb", "http://b/base/a%2Fb"},
		{"target without path", "http://b", "http://x.example/", "http://b/"},
		{"https backend", "https://b", "http://x.example/a", "https://b/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.url, nil)
			r.Close = true
			out := outgoingRequest(r, mustParse(t, tt.target))
			if got := out.URL.String(); got != tt.wantURL {
				t.Errorf("URL = %q, want %q", got, tt.wantURL)
			}
			if out.Host != "x.example" || out.RequestURI != "" || out.Close {
				t.Errorf("Host %q, RequestURI %q, Close %v; want x.example, empty, false", out.Host, out.RequestURI, out.Close)
			}
		})
	}
}

// TestOutgoingRequestHeaders checks which headers reach the backend.
func TestOutgoingRequestHeaders(t *testing.T) {
	t.Parallel()
	// with returns the headers every backend request gets, plus extra.
	with := func(extra http.Header) http.Header {
		h := http.Header{
			"User-Agent":        {""},
			"X-Forwarded-For":   {"192.0.2.1"},
			"X-Forwarded-Host":  {"x.example"},
			"X-Forwarded-Proto": {"http"},
		}
		maps.Copy(h, extra)
		return h
	}
	tests := []struct {
		name       string
		header     http.Header
		remoteAddr string
		tls        bool
		want       http.Header
	}{
		{"plain request", nil, "", false, with(nil)},
		{"user agent kept", http.Header{"User-Agent": {"custom/1.0"}}, "", false, with(http.Header{"User-Agent": {"custom/1.0"}})},
		{
			"hop-by-hop headers removed",
			http.Header{"Connection": {"X-Custom"}, "X-Custom": {"v"}, "Keep-Alive": {"5"}, "Proxy-Authorization": {"secret"}, "Upgrade": {"websocket"}, "X-Keep": {"yes"}},
			"", false, with(http.Header{"X-Keep": {"yes"}}),
		},
		{"TE trailers kept", http.Header{"Te": {"gzip, Trailers"}}, "", false, with(http.Header{"Te": {"trailers"}})},
		{"TE without trailers dropped", http.Header{"Te": {"gzip"}}, "", false, with(nil)},
		{
			"spoofed forwarding headers replaced",
			http.Header{"X-Forwarded-For": {"6.6.6.6"}, "Forwarded": {"for=6.6.6.6"}, "X-Forwarded-Host": {"evil"}, "X-Forwarded-Proto": {"https"}},
			"", false, with(nil),
		},
		{"TLS request", nil, "", true, with(http.Header{"X-Forwarded-Proto": {"https"}})},
		{
			"remote address without port",
			http.Header{"X-Forwarded-For": {"6.6.6.6"}}, "unix", false,
			http.Header{"User-Agent": {""}, "X-Forwarded-Host": {"x.example"}, "X-Forwarded-Proto": {"http"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://x.example/", nil)
			maps.Copy(r.Header, tt.header)
			if tt.remoteAddr != "" {
				r.RemoteAddr = tt.remoteAddr
			}
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			out := outgoingRequest(r, mustParse(t, "http://b"))
			if !maps.EqualFunc(out.Header, tt.want, slices.Equal) {
				t.Errorf("headers = %v, want %v", out.Header, tt.want)
			}
		})
	}
}

// TestOutgoingRequestBody checks that an empty body is dropped and any other is kept.
func TestOutgoingRequestBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		wantBody bool
	}{
		{"empty body dropped", "", false},
		{"body kept", "hello", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://x.example/", strings.NewReader(tt.body))
			out := outgoingRequest(r, mustParse(t, "http://b"))
			if (out.Body != nil) != tt.wantBody {
				t.Fatalf("Body present = %v, want %v", out.Body != nil, tt.wantBody)
			}
			if tt.wantBody {
				if got, _ := io.ReadAll(out.Body); string(got) != tt.body {
					t.Errorf("body = %q, want %q", got, tt.body)
				}
			}
		})
	}
}

// TestSingleJoiningSlash checks that two paths are joined with exactly one slash.
func TestSingleJoiningSlash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want string
	}{
		{"empty base", "", "/b", "/b"},
		{"both slashes", "/a/", "/b", "/a/b"},
		{"no slashes", "/a", "b", "/a/b"},
		{"slash on b", "/a", "/b", "/a/b"},
		{"slash on a", "/a/", "b", "/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := singleJoiningSlash(tt.a, tt.b); got != tt.want {
				t.Errorf("singleJoiningSlash(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
