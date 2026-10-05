package httpcache

import (
	"net/http"
	"strings"
)

// cacheControl holds the directives of Cache-Control header lines, keyed by
// lowercase name (RFC 9111 section 5.2: names are case-insensitive). A
// directive without an argument maps to "".
type cacheControl map[string]string

// parseCacheControl parses every Cache-Control line in h. Quoted arguments
// are unquoted. For a repeated directive the first occurrence wins.
func parseCacheControl(h http.Header) cacheControl {
	cc := cacheControl{}
	for _, line := range h.Values("Cache-Control") {
		for directive := range strings.SplitSeq(line, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if _, seen := cc[name]; !seen {
				cc[name] = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
	}
	return cc
}

func (cc cacheControl) has(name string) bool {
	_, ok := cc[name]
	return ok
}
