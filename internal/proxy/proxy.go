// Package proxy is a minimal HTTP reverse proxy that forwards requests to a
// set of backend servers, picked round-robin.
//
// Not supported yet: protocol upgrades (WebSocket) and forwarding 1xx
// informational responses.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// hopHeaders apply to a single connection and must not be forwarded
// (RFC 9110, section 7.6.1).
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// ReverseProxy forwards each request to one of its servers, round-robin.
// The request path and query are forwarded unchanged and the original Host
// header is kept.
type ReverseProxy struct {
	name      string
	servers   []*url.URL
	transport http.RoundTripper
	next      atomic.Uint64
}

// New returns a proxy for the route name that forwards to servers using
// transport. servers must not be empty.
func New(name string, servers []*url.URL, transport http.RoundTripper) *ReverseProxy {
	return &ReverseProxy{name: name, servers: servers, transport: transport}
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := p.servers[(p.next.Add(1)-1)%uint64(len(p.servers))]

	outreq := p.outgoingRequest(r, target)
	if outreq.Body != nil {
		defer outreq.Body.Close()
	}

	// RoundTrip rather than http.Client: a proxy must pass backend
	// redirects through to the client, not follow them itself.
	resp, err := p.transport.RoundTrip(outreq)
	if err != nil {
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			return // The client went away; nobody is left to answer.
		}
		slog.Error("proxy error", "route", p.name, "server", target.Host, "error", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	removeHopByHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)

	// Trailers are sent after the body; announce the ones we know about.
	announced := len(resp.Trailer)
	if announced > 0 {
		names := make([]string, 0, announced)
		for k := range resp.Trailer {
			names = append(names, k)
		}
		w.Header().Add("Trailer", strings.Join(names, ", "))
	}

	w.WriteHeader(resp.StatusCode)

	if err := copyBody(w, resp.Body, flushImmediately(resp)); err != nil {
		if errors.Is(err, errBackendRead) {
			slog.Warn("proxy: backend response body interrupted", "route", p.name, "server", target.Host, "error", err)
		}
		// The status line is already sent, so the only way to tell the
		// client the response is incomplete is to close the connection.
		abortConnection(w)
		return
	}

	if len(resp.Trailer) == announced {
		copyHeader(w.Header(), resp.Trailer)
	} else {
		// Trailers the backend added without announcing them.
		for k, vv := range resp.Trailer {
			w.Header()[http.TrailerPrefix+k] = vv
		}
	}
}

// outgoingRequest builds the request sent to target from the incoming
// request r.
func (p *ReverseProxy) outgoingRequest(r *http.Request, target *url.URL) *http.Request {
	out := r.Clone(r.Context())
	if r.ContentLength == 0 {
		out.Body = nil // Lets the transport retry safely on stale connections.
	}
	out.RequestURI = "" // Must be empty in client requests.
	out.Close = false
	out.Host = r.Host

	out.URL.Scheme = target.Scheme
	out.URL.Host = target.Host
	out.URL.Path, out.URL.RawPath = joinPath(target, r.URL)
	switch {
	case target.RawQuery == "":
	case r.URL.RawQuery == "":
		out.URL.RawQuery = target.RawQuery
	default:
		out.URL.RawQuery = target.RawQuery + "&" + r.URL.RawQuery
	}

	removeHopByHopHeaders(out.Header)
	// "TE: trailers" is the one hop-by-hop value that must be forwarded,
	// e.g. for gRPC.
	if headerHasToken(r.Header, "Te", "trailers") {
		out.Header.Set("Te", "trailers")
	}
	// Keep Go's transport from adding its own User-Agent.
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "")
	}

	// Never trust Forwarded or X-Forwarded-* from the client; it could spoof  them.
	out.Header.Del("Forwarded")
	out.Header.Del("X-Forwarded-For")
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		out.Header.Set("X-Forwarded-For", ip)
	}
	out.Header.Set("X-Forwarded-Host", r.Host)
	if r.TLS != nil {
		out.Header.Set("X-Forwarded-Proto", "https")
	} else {
		out.Header.Set("X-Forwarded-Proto", "http")
	}
	return out
}

// joinPath appends the request path to the target's base path, keeping
// the request's original escaping (e.g. %2F stays encoded).
func joinPath(target, req *url.URL) (path, rawPath string) {
	if target.RawPath == "" && req.RawPath == "" {
		return singleJoiningSlash(target.Path, req.Path), ""
	}
	escaped := singleJoiningSlash(target.EscapedPath(), req.EscapedPath())
	unescaped, err := url.PathUnescape(escaped)
	if err != nil {
		return singleJoiningSlash(target.Path, req.Path), ""
	}
	return unescaped, escaped
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case a == "":
		return b
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// removeHopByHopHeaders deletes the standard hop-by-hop headers and any
// header listed in the Connection header.
func removeHopByHopHeaders(h http.Header) {
	for _, v := range h.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// headerHasToken reports whether the comma-separated values of header key
// contain token, ignoring case.
func headerHasToken(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// flushImmediately reports whether each chunk of the response body should
// be sent to the client as soon as it arrives: for server-sent events and
// streamed responses of unknown length.
func flushImmediately(resp *http.Response) bool {
	// For Server-Sent Events responses, flush immediately.
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" || resp.ContentLength == -1 {
		return true
	}

	return false
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

var errBackendRead = errors.New("read from backend")

// copyBody copies the backend body to the client, flushing after each write
// when flush is true. Read errors are wrapped with errBackendRead.
func copyBody(w http.ResponseWriter, body io.Reader, flush bool) error {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
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
