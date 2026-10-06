package proxy

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// writeResponse sends resp to the client: headers, body, then trailers. A
// body that fails part-way aborts the client connection.
func (p *ReverseProxy) writeResponse(w http.ResponseWriter, r *http.Request, target *url.URL, resp *http.Response) {
	// Any 5xx status is a server error.
	if resp.StatusCode >= http.StatusInternalServerError {
		slog.WarnContext(r.Context(), "backend returned a server error", "route", p.name, "server", target.Host, "status", resp.StatusCode)
	}

	removeHopByHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)
	announced := announceTrailers(w.Header(), resp.Trailer)
	w.WriteHeader(resp.StatusCode)

	if err := copyBody(w, resp.Body, flushImmediately(resp)); err != nil {
		if errors.Is(err, errBackendRead) {
			slog.WarnContext(r.Context(), "proxy: backend response body interrupted", "route", p.name, "server", target.Host, "error", err)
		}
		// The status line is already sent, so the only way to tell the
		// client the response is incomplete is to close the connection.
		abortConnection(w)
		return
	}
	copyTrailers(w.Header(), resp.Trailer, announced)
}

// announceTrailers lists the trailers known before the body in the Trailer
// header h, since they are sent after it, and returns how many there are.
func announceTrailers(h, trailer http.Header) int {
	if len(trailer) == 0 {
		return 0
	}
	names := make([]string, 0, len(trailer))
	for k := range trailer {
		names = append(names, k)
	}
	h.Add("Trailer", strings.Join(names, ", "))
	return len(names)
}

// copyTrailers sets the trailers into h after the body. When the backend
// added trailers it did not announce, all are sent with http.TrailerPrefix.
func copyTrailers(h, trailer http.Header, announced int) {
	if len(trailer) == announced {
		copyHeader(h, trailer)
		return
	}
	for k, vv := range trailer {
		h[http.TrailerPrefix+k] = vv
	}
}

// flushImmediately reports whether each chunk of the response body should
// be sent to the client as soon as it arrives: for server-sent events and
// streamed responses of unknown length.
func flushImmediately(resp *http.Response) bool {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mediaType == "text/event-stream" || resp.ContentLength == -1
}

// abortConnection closes the client connection so a partly sent response
// is seen as broken, not as complete. Without this, a chunked response would
// be terminated normally and look whole to the client. Connections that
// cannot be hijacked (HTTP/2) are left to the server to finish.
func abortConnection(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	conn.Close()
}

// copyBufferSize is the maximum number of response body bytes copied per
// read and write. Reads return as soon as any bytes arrive, so this never
// delays small chunks.
const copyBufferSize = 32 * 1024

var errBackendRead = errors.New("read from backend")

// copyBuffers holds the buffers copyBody reads into, so each response does
// not allocate its own. It stores pointers, so Put does not allocate.
var copyBuffers = sync.Pool{New: func() any {
	buf := make([]byte, copyBufferSize)
	return &buf
}}

// copyBody copies the backend body to the client, flushing after each write
// when flush is true. Read errors are wrapped with errBackendRead.
func copyBody(w http.ResponseWriter, body io.Reader, flush bool) error {
	rc := http.NewResponseController(w)
	bufp := copyBuffers.Get().(*[]byte)
	defer copyBuffers.Put(bufp)
	buf := *bufp
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flush {
				if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
					return err
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("%w: %w", errBackendRead, rerr)
		}
	}
}
