package middleware

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"time"
)

// recorder passes the response through to the client unchanged. It records
// the final status, the headers as sent and when they were sent, and keeps
// a copy of the body when shouldStore approves the response.
type recorder struct {
	http.ResponseWriter

	// shouldStore decides from the status and headers whether the response
	// may be stored. When nil, the body is never copied.
	shouldStore func(status int, header http.Header) bool

	// cacheStatus, when set, is appended to the response's Cache-Status
	// header as the headers are sent, after any entries from the backend.
	cacheStatus string

	status       int
	header       http.Header
	responseTime time.Time
	recording    bool
	body         bytes.Buffer
	hijacked     bool
}

func (rec *recorder) WriteHeader(code int) {
	// 1xx responses are interim; only the first final status counts.
	if rec.status == 0 && code >= 200 {
		rec.status = code
		rec.header = rec.ResponseWriter.Header().Clone()
		rec.responseTime = time.Now()
		rec.recording = rec.shouldStore != nil && rec.shouldStore(code, rec.header)
		if rec.cacheStatus != "" {
			rec.ResponseWriter.Header().Add("Cache-Status", rec.cacheStatus)
		}
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.WriteHeader(http.StatusOK)
	}
	n, err := rec.ResponseWriter.Write(b)
	if rec.recording {
		rec.body.Write(b[:n]) // Copy exactly what the client received.
	}
	return n, err
}

// Unwrap gives http.ResponseController access to the underlying writer, so
// flushing and deadlines keep working.
func (rec *recorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}

// Hijack is how the proxy aborts a broken response. It is recorded, so the
// truncated response is not stored.
func (rec *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(rec.ResponseWriter).Hijack()
	if err == nil {
		rec.hijacked = true
	}
	return conn, brw, err
}

// storable reports whether the recorded response was approved for storing
// and arrived complete: the connection was not aborted (a truncated body).
func (rec *recorder) storable() bool {
	return rec.recording && !rec.hijacked
}
