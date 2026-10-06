package proxy

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// plainWriter is a ResponseWriter that cannot flush.
type plainWriter struct {
	bytes.Buffer
	header http.Header
}

func (w *plainWriter) Header() http.Header { return w.header }
func (w *plainWriter) WriteHeader(int)     {}

// flushFailingWriter is a ResponseWriter whose flushes fail.
type flushFailingWriter struct{ plainWriter }

var errFlush = errors.New("flush failed")

func (*flushFailingWriter) FlushError() error { return errFlush }

// failingWriter is a ResponseWriter whose writes fail.
type failingWriter struct{ plainWriter }

var errWrite = errors.New("write failed")

func (*failingWriter) Write([]byte) (int, error) { return 0, errWrite }

// failingReader returns data, then err.
type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// trailerBody is a response body that fills in trailer values once read to
// the end, as net/http does.
type trailerBody struct {
	io.Reader
	trailer http.Header // resp.Trailer
	values  http.Header // set at EOF
}

func (b *trailerBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		maps.Copy(b.trailer, b.values)
	}
	return n, err
}

func (*trailerBody) Close() error { return nil }

// TestFlushImmediately checks that event streams and bodies of unknown length are flushed.
func TestFlushImmediately(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		contentType   string
		contentLength int64
		want          bool
	}{
		{"event stream", "text/event-stream", 10, true},
		{"event stream with parameters", "text/event-stream; charset=utf-8", 10, true},
		{"unknown length", "text/plain", -1, true},
		{"known length", "text/plain", 10, false},
		{"no content type", "", 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Content-Type": {tt.contentType}}, ContentLength: tt.contentLength}
			if got := flushImmediately(resp); got != tt.want {
				t.Errorf("flushImmediately(%q, length %d) = %v, want %v", tt.contentType, tt.contentLength, got, tt.want)
			}
		})
	}
}

// TestCopyBody checks copying, flushing and the errors copyBody returns.
func TestCopyBody(t *testing.T) {
	t.Parallel()

	// A body larger than one buffer arrives whole.
	t.Run("larger than the buffer", func(t *testing.T) {
		t.Parallel()
		body := strings.Repeat("x", 3*copyBufferSize+7)
		rec := httptest.NewRecorder()
		if err := copyBody(rec, strings.NewReader(body), false); err != nil {
			t.Fatalf("copyBody: %v", err)
		}
		if rec.Body.String() != body || rec.Flushed {
			t.Errorf("copied %d bytes, flushed %v; want %d, false", rec.Body.Len(), rec.Flushed, len(body))
		}
	})
	// With flush set, written data is flushed.
	t.Run("flush", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		if err := copyBody(rec, strings.NewReader("data"), true); err != nil || !rec.Flushed {
			t.Errorf("copyBody = %v, flushed %v; want nil, true", err, rec.Flushed)
		}
	})
	// A writer that cannot flush is not an error.
	t.Run("flush not supported", func(t *testing.T) {
		t.Parallel()
		w := &plainWriter{header: http.Header{}}
		if err := copyBody(w, strings.NewReader("data"), true); err != nil || w.String() != "data" {
			t.Errorf("copyBody = %v, wrote %q; want nil, %q", err, w.String(), "data")
		}
	})
	// A flush that fails for another reason is returned.
	t.Run("flush error", func(t *testing.T) {
		t.Parallel()
		w := &flushFailingWriter{plainWriter{header: http.Header{}}}
		if err := copyBody(w, strings.NewReader("data"), true); !errors.Is(err, errFlush) {
			t.Errorf("copyBody = %v, want %v", err, errFlush)
		}
	})
	// A failed read from the backend is wrapped with errBackendRead.
	t.Run("read error", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		err := copyBody(rec, &failingReader{data: "part", err: io.ErrUnexpectedEOF}, false)
		if !errors.Is(err, errBackendRead) || !errors.Is(err, io.ErrUnexpectedEOF) || rec.Body.String() != "part" {
			t.Errorf("copyBody = %v, wrote %q; want errBackendRead wrapping unexpected EOF, %q", err, rec.Body.String(), "part")
		}
	})
	// A failed write to the client is returned as is.
	t.Run("write error", func(t *testing.T) {
		t.Parallel()
		w := &failingWriter{plainWriter{header: http.Header{}}}
		if err := copyBody(w, strings.NewReader("data"), false); !errors.Is(err, errWrite) || errors.Is(err, errBackendRead) {
			t.Errorf("copyBody = %v, want %v", err, errWrite)
		}
	})
}

// TestWriteResponse checks the status, headers, body and trailers sent to the client.
func TestWriteResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      int
		header      http.Header
		announced   []string    // trailer names known before the body
		trailers    http.Header // trailer values set at the end of the body
		body        io.Reader
		wantHeader  http.Header // checked keys only
		wantAbsent  []string
		wantBody    string
		wantTrailer http.Header
	}{
		{
			name:       "status, headers and body",
			status:     http.StatusCreated,
			header:     http.Header{"Connection": {"X-Hop"}, "X-Hop": {"1"}, "Keep-Alive": {"5"}, "X-Out": {"ok"}},
			body:       strings.NewReader("body"),
			wantHeader: http.Header{"X-Out": {"ok"}},
			wantAbsent: []string{"Connection", "X-Hop", "Keep-Alive", "Trailer"},
			wantBody:   "body",
		},
		{
			name:     "backend server error passed on",
			status:   http.StatusServiceUnavailable,
			body:     strings.NewReader("down"),
			wantBody: "down",
		},
		{
			name:        "announced trailers",
			status:      http.StatusOK,
			announced:   []string{"X-A"},
			trailers:    http.Header{"X-A": {"a"}},
			body:        strings.NewReader("body"),
			wantHeader:  http.Header{"Trailer": {"X-A"}},
			wantBody:    "body",
			wantTrailer: http.Header{"X-A": {"a"}},
		},
		{
			name:        "unannounced trailer",
			status:      http.StatusOK,
			announced:   []string{"X-A"},
			trailers:    http.Header{"X-A": {"a"}, "X-B": {"b"}},
			body:        strings.NewReader("body"),
			wantBody:    "body",
			wantTrailer: http.Header{"X-A": {"a"}, "X-B": {"b"}},
		},
		{
			name:        "body fails part-way",
			status:      http.StatusOK,
			announced:   []string{"X-A"},
			trailers:    http.Header{"X-A": {"a"}},
			body:        &failingReader{data: "part", err: io.ErrUnexpectedEOF},
			wantBody:    "part",
			wantTrailer: http.Header{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.status, Header: tt.header, Trailer: http.Header{}, ContentLength: -1}
			if resp.Header == nil {
				resp.Header = http.Header{}
			}
			for _, name := range tt.announced {
				resp.Trailer[name] = nil
			}
			resp.Body = &trailerBody{Reader: tt.body, trailer: resp.Trailer, values: tt.trailers}
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "http://x.example/", nil)

			New("r", nil, nil).writeResponse(rec, r, mustParse(t, "http://b"), resp)

			res := rec.Result()
			if res.StatusCode != tt.status || rec.Body.String() != tt.wantBody {
				t.Errorf("got %d %q, want %d %q", res.StatusCode, rec.Body.String(), tt.status, tt.wantBody)
			}
			for k, v := range tt.wantHeader {
				if got := res.Header.Values(k); !slices.Equal(got, v) {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			for _, k := range tt.wantAbsent {
				if res.Header.Get(k) != "" {
					t.Errorf("header %s = %q, want none", k, res.Header.Get(k))
				}
			}
			if tt.wantTrailer != nil && !maps.EqualFunc(res.Trailer, tt.wantTrailer, slices.Equal) {
				t.Errorf("trailers = %v, want %v", res.Trailer, tt.wantTrailer)
			}
		})
	}
}
