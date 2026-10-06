package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// roundTripFunc is an http.RoundTripper that calls itself, standing in for
// the backend.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// headerSpy records whether WriteHeader was called.
type headerSpy struct {
	*httptest.ResponseRecorder
	wroteHeader bool
}

func (s *headerSpy) WriteHeader(code int) {
	s.wroteHeader = true
	s.ResponseRecorder.WriteHeader(code)
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

// TestPick checks that servers are picked round-robin, starting with the first.
func TestPick(t *testing.T) {
	t.Parallel()
	p := New("r", []*url.URL{mustParse(t, "http://a"), mustParse(t, "http://b"), mustParse(t, "http://c")}, nil)
	var got []string
	for range 5 {
		got = append(got, p.pick().Host)
	}
	if want := []string{"a", "b", "c", "a", "b"}; !slices.Equal(got, want) {
		t.Errorf("picked %v, want %v", got, want)
	}
}

// TestServeHTTP checks that a request is forwarded and the backend's response sent back.
func TestServeHTTP(t *testing.T) {
	t.Parallel()
	var gotURL, gotBody string
	p := New("r", []*url.URL{mustParse(t, "http://b/base")}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{
			StatusCode:    http.StatusAccepted,
			Header:        http.Header{"X-Out": {"ok"}},
			Body:          io.NopCloser(strings.NewReader("from backend")),
			ContentLength: 12,
		}, nil
	}))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://x.example/a?x=1", strings.NewReader("to backend")))

	if gotURL != "http://b/base/a?x=1" || gotBody != "to backend" {
		t.Errorf("backend got %q with body %q, want %q with %q", gotURL, gotBody, "http://b/base/a?x=1", "to backend")
	}
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-Out") != "ok" || rec.Body.String() != "from backend" {
		t.Errorf("response = %d, X-Out %q, %q; want %d, ok, %q", rec.Code, rec.Header().Get("X-Out"), rec.Body.String(), http.StatusAccepted, "from backend")
	}
}

// TestServeHTTPBackendError checks the answer when no response comes back from the backend.
func TestServeHTTPBackendError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		err             error
		clientCancelled bool
		wantStatus      int // 0: nothing written
	}{
		{"backend unreachable", errors.New("dial tcp: connection refused"), false, http.StatusBadGateway},
		{"backend cancelled the request", context.Canceled, false, http.StatusBadGateway},
		{"client went away", context.Canceled, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New("r", []*url.URL{mustParse(t, "http://b")}, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, tt.err
			}))
			r := httptest.NewRequest(http.MethodGet, "http://x.example/", nil)
			if tt.clientCancelled {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			spy := &headerSpy{ResponseRecorder: httptest.NewRecorder()}
			p.ServeHTTP(spy, r)

			gotStatus := 0
			if spy.wroteHeader {
				gotStatus = spy.Code
			}
			if gotStatus != tt.wantStatus || spy.Body.Len() != 0 {
				t.Errorf("status = %d, body %q; want %d, empty", gotStatus, spy.Body.String(), tt.wantStatus)
			}
		})
	}
}
