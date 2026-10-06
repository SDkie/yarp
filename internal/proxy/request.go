package proxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// outgoingRequest builds the request sent to target from the incoming
// request r.
func outgoingRequest(r *http.Request, target *url.URL) *http.Request {
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
	if headerValuesContains(r.Header, "Te", "trailers") {
		out.Header.Set("Te", "trailers")
	}
	// Keep Go's transport from adding its own User-Agent.
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "")
	}

	// Never trust Forwarded or X-Forwarded-* from the client; it could spoof them.
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
