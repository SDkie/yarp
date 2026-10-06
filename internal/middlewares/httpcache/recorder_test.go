package httpcache

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// fakeWriter is a ResponseWriter that records what it receives. Unlike
// httptest.ResponseRecorder it passes interim 1xx responses through. A
// maxWrite above 0 makes Write accept at most that many bytes.
type fakeWriter struct {
	header    http.Header
	codes     []int
	body      bytes.Buffer
	maxWrite  int
	canHijack bool
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{header: http.Header{}}
}

func (f *fakeWriter) Header() http.Header { return f.header }

func (f *fakeWriter) WriteHeader(code int) { f.codes = append(f.codes, code) }

func (f *fakeWriter) Write(b []byte) (int, error) {
	if f.maxWrite > 0 && len(b) > f.maxWrite {
		n, _ := f.body.Write(b[:f.maxWrite])
		return n, errors.New("short write")
	}
	return f.body.Write(b)
}

func (f *fakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !f.canHijack {
		return nil, nil, http.ErrNotSupported
	}
	client, server := net.Pipe()
	client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

// approve is a shouldStore that approves every response.
func approve(int, http.Header) bool { return true }

// TestRecorderStatus checks which status the recorder keeps.
func TestRecorderStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(rec *recorder)
		want  int
		codes []int // seen by the client's writer
	}{
		{"implicit 200", func(rec *recorder) { io.WriteString(rec, "x") }, http.StatusOK, []int{http.StatusOK}},
		{"explicit", func(rec *recorder) { rec.WriteHeader(http.StatusNotFound) }, http.StatusNotFound, []int{http.StatusNotFound}},
		{"1xx is interim", func(rec *recorder) { rec.WriteHeader(http.StatusEarlyHints); rec.WriteHeader(http.StatusOK) }, http.StatusOK, []int{http.StatusEarlyHints, http.StatusOK}},
		{"first final status wins", func(rec *recorder) { rec.WriteHeader(http.StatusOK); rec.WriteHeader(http.StatusInternalServerError) }, http.StatusOK, []int{http.StatusOK, http.StatusInternalServerError}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fw := newFakeWriter()
			rec := &recorder{ResponseWriter: fw}
			tt.write(rec)
			if rec.status != tt.want || !slices.Equal(fw.codes, tt.codes) {
				t.Errorf("status %d, client saw %v; want %d, %v", rec.status, fw.codes, tt.want, tt.codes)
			}
		})
	}
}

// TestRecorderCacheStatus checks where yarp's Cache-Status entry is added.
func TestRecorderCacheStatus(t *testing.T) {
	t.Parallel()
	fw := newFakeWriter()
	rec := &recorder{ResponseWriter: fw, cacheStatus: cacheStatusMiss, shouldStore: approve}
	rec.Header().Set("Cache-Status", "upstream; hit")
	rec.WriteHeader(http.StatusEarlyHints) // not added to interim responses
	if got := fw.header.Values("Cache-Status"); !slices.Equal(got, []string{"upstream; hit"}) {
		t.Errorf("after 103, Cache-Status = %q", got)
	}
	rec.WriteHeader(http.StatusOK)

	if got := fw.header.Values("Cache-Status"); !slices.Equal(got, []string{"upstream; hit", miss}) {
		t.Errorf("client's Cache-Status = %q, want yarp's entry last", got)
	}
	// The stored copy has only the backend's entry; hits add their own.
	if got := rec.header.Values("Cache-Status"); !slices.Equal(got, []string{"upstream; hit"}) {
		t.Errorf("stored Cache-Status = %q", got)
	}
}

// TestRecorderBody checks that the body is copied only when storing is approved.
func TestRecorderBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		shouldStore func(int, http.Header) bool
		want        string
	}{
		{"no shouldStore", nil, ""},
		{"not approved", func(int, http.Header) bool { return false }, ""},
		{"approved", approve, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fw := newFakeWriter()
			rec := &recorder{ResponseWriter: fw, shouldStore: tt.shouldStore}
			io.WriteString(rec, "ab")
			io.WriteString(rec, "c")
			if fw.body.String() != "abc" || rec.body.String() != tt.want {
				t.Errorf("client got %q, copy %q; want %q, %q", fw.body.String(), rec.body.String(), "abc", tt.want)
			}
			if rec.storable() != (tt.want != "") {
				t.Errorf("storable = %v", rec.storable())
			}
		})
	}
}

// TestRecorderCopiesWhatTheClientGot checks that a short write copies only the bytes written.
func TestRecorderCopiesWhatTheClientGot(t *testing.T) {
	t.Parallel()
	fw := newFakeWriter()
	fw.maxWrite = 2
	rec := &recorder{ResponseWriter: fw, shouldStore: approve}
	n, err := io.WriteString(rec, "abcde")
	if n != 2 || err == nil || rec.body.String() != "ab" {
		t.Errorf("Write = %d, %v; copy %q; want 2, an error, %q", n, err, rec.body.String(), "ab")
	}
}

// TestRecorderHeaderCopy checks the header copy and time taken when the headers are sent.
func TestRecorderHeaderCopy(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		fw := newFakeWriter()
		var seen http.Header
		rec := &recorder{ResponseWriter: fw, shouldStore: func(status int, h http.Header) bool {
			seen = h
			h.Set("Date", "stored only")
			return true
		}}
		rec.Header().Set("X-Before", "1")
		time.Sleep(5 * time.Second)
		rec.WriteHeader(http.StatusOK)
		rec.Header().Set("X-After", "1")

		if seen.Get("X-Before") != "1" || seen.Get("X-After") != "" {
			t.Errorf("shouldStore saw %v, want the headers as sent", seen)
		}
		if rec.header.Get("Date") != "stored only" || fw.header.Get("Date") != "" {
			t.Errorf("Date: stored %q, client %q; want the change only in the copy", rec.header.Get("Date"), fw.header.Get("Date"))
		}
		if want := start.Add(5 * time.Second); !rec.responseTime.Equal(want) {
			t.Errorf("responseTime = %v, want %v", rec.responseTime, want)
		}
	})
}

// TestRecorderHijack checks that a hijacked response is not storable.
func TestRecorderHijack(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		canHijack bool
	}{
		{"supported", true},
		{"not supported", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fw := newFakeWriter()
			fw.canHijack = tt.canHijack
			rec := &recorder{ResponseWriter: fw, shouldStore: approve}
			io.WriteString(rec, "part")

			conn, _, err := http.NewResponseController(rec).Hijack()
			if tt.canHijack {
				if err != nil {
					t.Fatalf("Hijack: %v", err)
				}
				conn.Close()
			} else if !errors.Is(err, http.ErrNotSupported) {
				t.Fatalf("Hijack = %v, want ErrNotSupported", err)
			}
			// A hijacked connection means a truncated response.
			if got := rec.storable(); got == tt.canHijack {
				t.Errorf("storable = %v, want %v", got, !tt.canHijack)
			}
		})
	}
}

// TestRecorderUnwrap checks that Unwrap returns the wrapped writer.
func TestRecorderUnwrap(t *testing.T) {
	t.Parallel()
	fw := newFakeWriter()
	if got := (&recorder{ResponseWriter: fw}).Unwrap(); got != fw {
		t.Errorf("Unwrap = %v, want the wrapped writer", got)
	}
}
