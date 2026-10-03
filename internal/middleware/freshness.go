package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxDeltaSeconds caps delta-seconds values (RFC 9111 section 1.2.2).
const maxDeltaSeconds = 1 << 31

// getFreshness returns when the response was generated and when it becomes
// stale (RFC 9111 section 4.2). header must carry a valid Date.
func getFreshness(header http.Header, cc cacheControl, requestTime, responseTime time.Time) (generatedAt, expiresAt time.Time) {
	generatedAt = responseTime.Add(-initialAge(header, requestTime, responseTime))
	return generatedAt, generatedAt.Add(freshnessLifetime(header, cc))
}

// initialAge returns the response's age on arrival: Age plus the time the
// backend took (RFC 9111 section 4.2.3). Date is not used, so clock skew
// between yarp and the backend does not matter.
func initialAge(header http.Header, requestTime, responseTime time.Time) time.Duration {
	first, _, _ := strings.Cut(header.Get("Age"), ",")
	return parseDeltaSeconds(strings.TrimSpace(first)) + responseTime.Sub(requestTime)
}

// freshnessLifetime returns s-maxage, max-age or Expires minus Date, in that
// order (RFC 9111 section 4.2.1). Without any, or if invalid, it is 0.
func freshnessLifetime(header http.Header, cc cacheControl) time.Duration {
	if v, ok := cc["s-maxage"]; ok {
		return parseDeltaSeconds(v)
	}
	if v, ok := cc["max-age"]; ok {
		return parseDeltaSeconds(v)
	}
	if len(header.Values("Expires")) == 0 {
		return 0
	}
	// An invalid Expires, such as "0", means expired (section 5.3).
	expires, err := http.ParseTime(header.Get("Expires"))
	if err != nil {
		return 0
	}
	date, err := http.ParseTime(header.Get("Date"))
	if err != nil {
		return 0
	}
	return max(0, expires.Sub(date))
}

// parseDeltaSeconds parses delta-seconds; invalid values yield 0.
func parseDeltaSeconds(v string) time.Duration {
	secs, err := strconv.ParseUint(v, 10, 64)
	if errors.Is(err, strconv.ErrRange) || secs > maxDeltaSeconds {
		secs = maxDeltaSeconds
	} else if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}
