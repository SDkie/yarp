package httpcache

import (
	"net/http"
	"strings"
	"time"
)

// bypassesCache reports whether r must skip the cache entirely: Range
// requests (yarp does not store partial content) and preconditions that
// only the origin can evaluate.
func bypassesCache(r *http.Request) bool {
	bypassHeaders := [...]string{
		"Range",
		"If-Match",
		"If-Unmodified-Since",
		"If-Range",
	}
	for _, name := range bypassHeaders {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}

// checkStorable reports whether the response to r, with status and header,
// may be stored and, if so, when it was generated and when it becomes stale.
// r was forwarded at requestTime and the response arrived at responseTime.
// A missing or invalid Date in header is replaced.
func checkStorable(r *http.Request, status int, header http.Header, requestTime, responseTime time.Time) (generatedAt, expiresAt time.Time, ok bool) {
	cc := parseCacheControl(header)
	if !isStorable(r, status, header, cc) {
		return time.Time{}, time.Time{}, false
	}
	// A stored response must carry a valid Date (RFC 9110 section 6.6.1):
	// without one, hits would be sent with the time they are served, and
	// with an invalid one, hits would replay it. Either is replaced by the
	// time the response was received.
	if _, err := http.ParseTime(header.Get("Date")); err != nil {
		header.Set("Date", responseTime.UTC().Format(http.TimeFormat))
	}
	generatedAt, expiresAt = getFreshness(header, cc, requestTime, responseTime)
	// Only responses still fresh on arrival can ever be served.
	return generatedAt, expiresAt, expiresAt.After(responseTime)
}

// isStorable reports whether the response to r with status, header and its
// parsed Cache-Control cc may be stored.
func isStorable(r *http.Request, status int, header http.Header, cc cacheControl) bool {
	switch {
	case status == http.StatusPartialContent, status == http.StatusNotModified:
		return false // Partial or body-less: never a complete response.
	case status >= 500:
		return false // Server errors are never cached.
	}
	if _, ok := getVaryNames(header); !ok {
		return false // Vary: * never matches a later request.
	}
	if ct, _, _ := strings.Cut(header.Get("Content-Type"), ";"); strings.TrimSpace(ct) == "text/event-stream" {
		return false // An endless stream would be copied forever.
	}
	if cc.has("no-store") || cc.has("private") {
		return false
	}
	if cc.has("no-cache") {
		return false // Must be validated before each use (section 5.2.2.4).
	}
	if len(header.Values("Set-Cookie")) > 0 {
		return false // A user's cookie must not reach others (section 7.3).
	}
	if r.Header.Get("Authorization") != "" {
		return cc.has("public") || cc.has("s-maxage") || cc.has("must-revalidate")
	}
	return true
}
