package httpcache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBypassesCache checks which request headers bypass the cache.
func TestBypassesCache(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"none", "", false},
		{"Range", "Range", true},
		{"If-Match", "If-Match", true},
		{"If-Unmodified-Since", "If-Unmodified-Since", true},
		{"If-Range", "If-Range", true},
		{"If-None-Match", "If-None-Match", false},         // answered from the cache
		{"If-Modified-Since", "If-Modified-Since", false}, // answered from the cache
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				r.Header.Set(tt.header, "x")
			}
			if got := bypassesCache(r); got != tt.want {
				t.Errorf("bypassesCache with %q = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// TestIsStorable checks the rules for storing a response.
func TestIsStorable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		header []string
		auth   bool // the request has Authorization
		want   bool
	}{
		{"200", http.StatusOK, nil, false, true},
		{"404", http.StatusNotFound, nil, false, true},
		{"301", http.StatusMovedPermanently, nil, false, true},
		{"206", http.StatusPartialContent, nil, false, false},
		{"304", http.StatusNotModified, nil, false, false},
		{"500", http.StatusInternalServerError, nil, false, false},
		{"Vary *", http.StatusOK, []string{"Vary", "*"}, false, false},
		{"Vary list", http.StatusOK, []string{"Vary", "Accept"}, false, true},
		{"event stream", http.StatusOK, []string{"Content-Type", "text/event-stream; charset=utf-8"}, false, false},
		{"no-store", http.StatusOK, []string{"Cache-Control", "no-store"}, false, false},
		{"private", http.StatusOK, []string{"Cache-Control", "private"}, false, false},
		{"no-cache", http.StatusOK, []string{"Cache-Control", "no-cache"}, false, false},
		{"Set-Cookie", http.StatusOK, []string{"Set-Cookie", "a=b"}, false, false},
		{"Authorization", http.StatusOK, nil, true, false},
		{"Authorization, public", http.StatusOK, []string{"Cache-Control", "public"}, true, true},
		{"Authorization, s-maxage", http.StatusOK, []string{"Cache-Control", "s-maxage=60"}, true, true},
		{"Authorization, must-revalidate", http.StatusOK, []string{"Cache-Control", "must-revalidate"}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "/", nil)
			if tt.auth {
				r.Header.Set("Authorization", "Bearer x")
			}
			h := headerOf(tt.header...)
			if got := isStorable(r, tt.status, h, parseCacheControl(h)); got != tt.want {
				t.Errorf("isStorable = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckStorable checks the store decision, the freshness times and the Date fix.
func TestCheckStorable(t *testing.T) {
	t.Parallel()
	requestTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	responseTime := requestTime.Add(2 * time.Second)
	r := httptest.NewRequest("GET", "/", nil)

	// A rejected response returns zero times.
	t.Run("not storable", func(t *testing.T) {
		t.Parallel()
		generatedAt, expiresAt, ok := checkStorable(r, http.StatusOK, headerOf("Cache-Control", "no-store, max-age=60"), requestTime, responseTime)
		if ok || !generatedAt.IsZero() || !expiresAt.IsZero() {
			t.Errorf("got %v, %v, %v; want zero times, false", generatedAt, expiresAt, ok)
		}
	})

	// A fresh response's times count the backend's 2s as age.
	t.Run("fresh", func(t *testing.T) {
		t.Parallel()
		h := headerOf("Cache-Control", "max-age=60")
		generatedAt, expiresAt, ok := checkStorable(r, http.StatusOK, h, requestTime, responseTime)
		// The 2s the backend took count as age.
		if !ok || !generatedAt.Equal(requestTime) || !expiresAt.Equal(requestTime.Add(time.Minute)) {
			t.Errorf("got %v, %v, %v; want %v, %v, true", generatedAt, expiresAt, ok, requestTime, requestTime.Add(time.Minute))
		}
	})

	// A response already stale on arrival is not stored.
	t.Run("stale on arrival", func(t *testing.T) {
		t.Parallel()
		h := headerOf("Cache-Control", "max-age=60", "Age", "58")
		if _, _, ok := checkStorable(r, http.StatusOK, h, requestTime, responseTime); ok {
			t.Error("a response 60s old on arrival was storable")
		}
	})

	// A response without freshness is not stored.
	t.Run("no freshness", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := checkStorable(r, http.StatusOK, http.Header{}, requestTime, responseTime); ok {
			t.Error("a response without freshness was storable")
		}
	})

	// A missing or invalid Date is replaced by the arrival time.
	for _, date := range []string{"", "nonsense"} {
		t.Run("Date "+date+" replaced", func(t *testing.T) {
			t.Parallel()
			h := headerOf("Cache-Control", "max-age=60", "Date", date)
			checkStorable(r, http.StatusOK, h, requestTime, responseTime)
			if got, want := h.Get("Date"), responseTime.Format(http.TimeFormat); got != want {
				t.Errorf("Date = %q, want %q", got, want)
			}
		})
	}

	// A valid Date is kept.
	t.Run("valid Date kept", func(t *testing.T) {
		t.Parallel()
		const date = "Fri, 31 Dec 1999 00:00:00 GMT"
		h := headerOf("Cache-Control", "max-age=60", "Date", date)
		checkStorable(r, http.StatusOK, h, requestTime, responseTime)
		if got := h.Get("Date"); got != date {
			t.Errorf("Date = %q, want %q", got, date)
		}
	})
}
