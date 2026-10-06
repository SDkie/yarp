package httpcache

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// TestMatchesETag checks weak ETag comparison against If-None-Match values.
func TestMatchesETag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values []string
		etag   string
		want   bool
	}{
		{"same", []string{`"v1"`}, `"v1"`, true},
		{"weak request", []string{`W/"v1"`}, `"v1"`, true},
		{"weak stored", []string{`"v1"`}, `W/"v1"`, true},
		{"in a list", []string{`"x", "v1"`}, `"v1"`, true},
		{"in a later line", []string{`"x"`, `"v1"`}, `"v1"`, true},
		{"different", []string{`"x"`}, `"v1"`, false},
		{"star", []string{"*"}, `"v1"`, true},
		{"star without ETag", []string{"*"}, "", true},
		{"no stored ETag", []string{`"v1"`}, "", false},
		{"empty value", []string{""}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchesETag(tt.values, tt.etag); got != tt.want {
				t.Errorf("matchesETag(%q, %q) = %v, want %v", tt.values, tt.etag, got, tt.want)
			}
		})
	}
}

// TestIsNotModified checks when a stored response may be answered with 304.
func TestIsNotModified(t *testing.T) {
	t.Parallel()
	const (
		date     = "Sat, 01 Jan 2000 00:00:00 GMT"
		modified = "Fri, 31 Dec 1999 00:00:00 GMT"
		before   = "Thu, 30 Dec 1999 00:00:00 GMT"
	)
	stored := func(status int, header ...string) *record {
		return &record{Status: status, Header: headerOf(header...)}
	}
	tests := []struct {
		name   string
		e      *record
		header []string
		want   bool
	}{
		{"no conditions", stored(http.StatusOK, "ETag", `"v1"`), nil, false},
		{"If-None-Match", stored(http.StatusOK, "ETag", `"v1"`), []string{"If-None-Match", `"v1"`}, true},
		{"If-None-Match on a 301", stored(http.StatusMovedPermanently, "ETag", `"v1"`), []string{"If-None-Match", `"v1"`}, false},
		{"If-None-Match wins", stored(http.StatusOK, "ETag", `"v1"`, "Last-Modified", modified), []string{"If-None-Match", `"x"`, "If-Modified-Since", date}, false},
		{"If-Modified-Since equal", stored(http.StatusOK, "Last-Modified", modified), []string{"If-Modified-Since", modified}, true},
		{"If-Modified-Since before", stored(http.StatusOK, "Last-Modified", modified), []string{"If-Modified-Since", before}, false},
		{"If-Modified-Since falls back to Date", stored(http.StatusOK, "Date", date), []string{"If-Modified-Since", date}, true},
		{"If-Modified-Since, invalid Last-Modified and Date", stored(http.StatusOK, "Last-Modified", "x", "Date", "y"), []string{"If-Modified-Since", date}, false},
		{"invalid If-Modified-Since", stored(http.StatusOK, "Last-Modified", modified), []string{"If-Modified-Since", "yesterday"}, false},
		{"two If-Modified-Since", stored(http.StatusOK, "Last-Modified", modified), []string{"If-Modified-Since", date, "If-Modified-Since", date}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "/", nil)
			for i := 0; i+1 < len(tt.header); i += 2 {
				r.Header.Add(tt.header[i], tt.header[i+1])
			}
			if got := isNotModified(r, tt.e); got != tt.want {
				t.Errorf("isNotModified = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWriteEntry checks that a hit replays the stored status, headers and body.
func TestWriteEntry(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				e := &record{
					Status:      http.StatusNotFound,
					Header:      headerOf("Content-Type", "text/plain", "Cache-Status", "upstream; hit"),
					Body:        []byte("gone"),
					GeneratedAt: time.Now().Add(-90 * time.Second),
				}
				w := httptest.NewRecorder()
				writeEntry(w, httptest.NewRequest(method, "/", nil), e)

				wantBody := "gone"
				if method == "HEAD" {
					wantBody = ""
				}
				if w.Code != http.StatusNotFound || w.Body.String() != wantBody {
					t.Errorf("got %d %q, want 404 %q", w.Code, w.Body, wantBody)
				}
				if got := w.Header().Get("Content-Type"); got != "text/plain" {
					t.Errorf("Content-Type = %q", got)
				}
				if got := w.Header().Values("Cache-Status"); !slices.Equal(got, []string{"upstream; hit", hit}) {
					t.Errorf("Cache-Status = %q", got)
				}
				if got := w.Header().Get("Age"); got != "90" {
					t.Errorf("Age = %q, want 90", got)
				}
			})
		})
	}
}

// TestWriteNotModified checks that a 304 carries only the allowed stored headers.
func TestWriteNotModified(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		e := &record{
			Status: http.StatusOK,
			Header: headerOf(
				"Cache-Control", "max-age=60",
				"Content-Location", "/a.en",
				"Date", "Sat, 01 Jan 2000 00:00:00 GMT",
				"ETag", `"v1"`,
				"Expires", "Sat, 01 Jan 2000 00:01:00 GMT",
				"Last-Modified", "Fri, 31 Dec 1999 00:00:00 GMT",
				"Vary", "Accept-Encoding",
				"Content-Type", "text/plain",
				"Content-Length", "4",
				"X-Extra", "1",
			),
			Body:        []byte("body"),
			GeneratedAt: time.Now(),
		}
		w := httptest.NewRecorder()
		writeNotModified(w, e)

		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("got %d with %d body bytes, want 304 and none", w.Code, w.Body.Len())
		}
		for _, name := range []string{"Cache-Control", "Content-Location", "Date", "ETag", "Expires", "Last-Modified", "Vary"} {
			if got, want := w.Header().Get(name), e.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		for _, name := range []string{"Content-Type", "Content-Length", "X-Extra"} {
			if got := w.Header().Get(name); got != "" {
				t.Errorf("%s = %q, want none", name, got)
			}
		}
		if w.Header().Get("Cache-Status") != hit || w.Header().Get("Age") != "0" {
			t.Errorf("Cache-Status %q, Age %q; want %q, 0", w.Header().Get("Cache-Status"), w.Header().Get("Age"), hit)
		}
	})
}

// TestAgeNeverNegative checks that Age is 0 when the clock was set back.
func TestAgeNeverNegative(t *testing.T) {
	t.Parallel()
	// Generated "in the future": the clock was set back since.
	e := &record{GeneratedAt: time.Now().Add(time.Hour)}
	w := httptest.NewRecorder()
	addHitHeaders(w, e)
	if got := w.Header().Get("Age"); got != "0" {
		t.Errorf("Age = %q, want 0", got)
	}
}
