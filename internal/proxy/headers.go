package proxy

import (
	"net/http"
	"strings"
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

// headerValuesContains reports whether the comma-separated values of header
// key contain value, ignoring case (RFC 9110, section 5.6.1).
func headerValuesContains(h http.Header, key, value string) bool {
	for _, v := range h.Values(key) {
		for item := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
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
