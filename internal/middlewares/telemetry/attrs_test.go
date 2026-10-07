package telemetry

import (
	"net/http"
	"net/url"
	"testing"

	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
)

// TestGetMethodAttr checks that standard methods are kept and others become _OTHER.
func TestGetMethodAttr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   httpconv.RequestMethodAttr
	}{
		{http.MethodGet, httpconv.RequestMethodGet},
		{http.MethodPost, httpconv.RequestMethodPost},
		{http.MethodConnect, httpconv.RequestMethodConnect},
		{"FOO", httpconv.RequestMethodOther},
		{"get", httpconv.RequestMethodOther},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			if got := getMethodAttr(tt.method); got != tt.want {
				t.Errorf("getMethodAttr(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

// TestGetSpanName checks that a span is named after its method, or HTTP for a non-standard one.
func TestGetSpanName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method httpconv.RequestMethodAttr
		want   string
	}{
		{httpconv.RequestMethodGet, "GET"},
		{httpconv.RequestMethodDelete, "DELETE"},
		{httpconv.RequestMethodOther, "HTTP"},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			t.Parallel()
			if got := getSpanName(tt.method); got != tt.want {
				t.Errorf("getSpanName(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

// TestGetPort checks that the URL's port is used, or else its scheme's default.
func TestGetPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want int
	}{
		{"http://a:9000/x", 9000},
		{"https://a:8443", 8443},
		{"http://a/x", 80},
		{"https://a/x", 443},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			if got := getPort(mustParse(t, tt.url)); got != tt.want {
				t.Errorf("getPort(%q) = %d, want %d", tt.url, got, tt.want)
			}
		})
	}
}

// TestGetURLWithoutQuery checks that user info, query and fragment are dropped, and u is left unchanged.
func TestGetURLWithoutQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want string
	}{
		{"http://a:9000/x", "http://a:9000/x"},
		{"http://user:secret@a/x", "http://a/x"},
		{"http://a/x?token=secret", "http://a/x"},
		{"http://a/x#frag", "http://a/x"},
		{"https://user@a/x/y?q=1#f", "https://a/x/y"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			u := mustParse(t, tt.url)
			if got := getURLWithoutQuery(u); got != tt.want {
				t.Errorf("getURLWithoutQuery(%q) = %q, want %q", tt.url, got, tt.want)
			}
			if u.String() != tt.url {
				t.Errorf("u changed to %q", u)
			}
		})
	}
}

// mustParse parses rawURL or fails the test.
func mustParse(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	return u
}
