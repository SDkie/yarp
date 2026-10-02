package middleware

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
)

// recorder passes the response through to the client unchanged while
// keeping a copy of its status, headers and body.
type recorder struct {
	http.ResponseWriter
	status   int
	header   http.Header
	body     bytes.Buffer
	hijacked bool
}

func (rec *recorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
		rec.header = rec.ResponseWriter.Header().Clone()
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.WriteHeader(http.StatusOK)
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.body.Write(b[:n]) // Copy exactly what the client received.
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

// storable reports whether the recorded response should be stored: a
// response was written, the connection was not aborted (a truncated body),
// and it is not a 502, which is what the proxy answers when the backend is
// unreachable.
func (rec *recorder) storable() bool {
	return rec.status != 0 && !rec.hijacked && rec.status != http.StatusBadGateway
}
