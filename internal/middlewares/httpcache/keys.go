package httpcache

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
)

// Each cached URL is kept as two kinds of entries in the Store, because the
// request headers that select a response (its Vary header) are only known
// once the response has arrived. Both are gob-encoded and expire from the
// store when the response becomes stale.
//
// A Vary record says which request headers select the URL's responses:
//
//	key:   "vary:" + hex(sha256(base))
//	base:  method, host, path and query, e.g. "GET example.com/products?page=2"
//	value: varyRecord{Key: base, Names: []string{"Accept-Encoding"}}
//
// A response record holds one response for one combination of those
// headers' values:
//
//	key:     "resp:" + hex(sha256(variant))
//	variant: base plus one line per Vary header name, in sorted order, e.g.
//	         "GET example.com/products?page=2\nAccept-Encoding: gzip,br";
//	         a header the request lacks gives "\nAccept-Encoding absent".
//	         Without a Vary header, variant is base.
//	value:   record{Key: variant, Status, Header, Body, GeneratedAt, ExpiresAt}
//
// A lookup reads the Vary record for base, builds variant from the request
// and reads that response record. Invalidating a URL deletes its GET and
// HEAD Vary records, which leaves its response records unreachable until
// they expire.
//
// Hashing keeps keys short and keeps URLs, which can carry tokens, out of
// key names. Each value stores its plain key, which is compared on read, so
// an entry whose hash collides with another URL or variant is never served.
const (
	varyPrefix = "vary:" // Vary records
	respPrefix = "resp:" // response records
)

// getVaryKey returns the storage key of the Vary record for method and r's
// URL, and the plain base key it is hashed from.
func getVaryKey(method string, r *http.Request) (key, plain string) {
	plain = getBaseKey(method, r)
	return getHashKey(varyPrefix, plain), plain
}

// getRespKey returns the storage key of the response stored for r and the
// plain key it is hashed from: the base key plus r's values for the Vary
// header names, one "\n"-separated line each. A header r does not send is
// marked absent, so it only matches requests that also lack it. Header
// values cannot contain "\n".
func getRespKey(r *http.Request, names []string) (key, plain string) {
	var b strings.Builder
	b.WriteString(getBaseKey(r.Method, r))
	for _, name := range names {
		values := r.Header.Values(name)
		b.WriteString("\n")
		b.WriteString(name)
		if len(values) == 0 {
			b.WriteString(" absent")
			continue
		}
		b.WriteString(": ")
		b.WriteString(normalizeValues(values))
	}
	plain = b.String()
	return getHashKey(respPrefix, plain), plain
}

// getBaseKey returns the plain base key of method and r's URL: method,
// host, path and query, e.g. "GET example.com/products?page=2".
func getBaseKey(method string, r *http.Request) string {
	return method + " " + r.Host + r.URL.RequestURI()
}

// getHashKey returns the storage key for a plain key: prefix plus the hex
// SHA-256 of plain, always len(prefix)+64 bytes.
func getHashKey(prefix, plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return prefix + hex.EncodeToString(sum[:])
}

// normalizeValues joins header field lines into one comma-separated value
// with the spaces around each element removed, so equivalent requests (RFC
// 9111 section 4.1) produce the same key.
func normalizeValues(values []string) string {
	var parts []string
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			parts = append(parts, strings.TrimSpace(part))
		}
	}
	return strings.Join(parts, ",")
}

// getVaryNames returns the sorted, canonical header names listed in h's Vary
// header. ok is false for "Vary: *", which means the response can never be
// reused for another request.
func getVaryNames(h http.Header) (names []string, ok bool) {
	for _, v := range h.Values("Vary") {
		for name := range strings.SplitSeq(v, ",") {
			name = strings.TrimSpace(name)
			switch name {
			case "":
				continue
			case "*":
				return nil, false
			}
			names = append(names, http.CanonicalHeaderKey(name))
		}
	}
	slices.Sort(names)
	return slices.Compact(names), true
}
