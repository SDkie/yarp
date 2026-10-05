// Package httpcache is a middleware that caches backend responses
// (RFC 9111) and marks each response with a Cache-Status (RFC 9211).
//
// It caches responses to GET and HEAD requests and replies from the cache
// when a response for the same method, host, path and query, and the same
// values of the request headers named in the response's Vary header, is
// stored. It follows these RFC 9111 and RFC 9211 rules:
//
//   - Only final, complete responses are stored, never 206 or 304
//     (sections 3, 3.3, 4.3.4). Requests with Range, If-Match,
//     If-Unmodified-Since or If-Range bypass the cache.
//   - A hit answers the client's If-None-Match or If-Modified-Since with 304
//     when the stored 200 matches (section 4.3.2).
//   - Cache-Control no-store and private responses are never stored
//     (sections 3, 5.2.2), nor responses with Set-Cookie (section 7.3), nor
//     responses to requests with Authorization unless public, s-maxage or
//     must-revalidate allows it (section 3.5).
//   - Only fresh responses are served (sections 4, 4.2). Freshness comes
//     from s-maxage, max-age or Expires; responses without it, and no-cache
//     responses, are not stored, as yarp has no heuristic freshness or
//     validation yet. Stale responses are fetched again, never served, and
//     stored responses expire from the store once stale.
//   - Responses served from the cache carry an Age header (section 4).
//   - A 2xx or 3xx response to an unsafe method invalidates the cached
//     GET and HEAD responses for its URL (section 4.4).
//   - Every response gets a Cache-Status entry: hit, miss or bypass (RFC
//     9211).
package httpcache
