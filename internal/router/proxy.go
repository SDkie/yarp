package router

import (
	"net/http"
	"net/url"

	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/proxy"
)

// newProxy returns a handler that forwards requests to servers, picking
// them round-robin. The request path is appended to the server's base path,
// the query is merged with the server's query, and the original Host header
// is kept.
func newProxy(route string, servers []config.Server, transport http.RoundTripper) http.Handler {
	targets := make([]*url.URL, len(servers))
	for i, s := range servers {
		targets[i], _ = url.Parse(s.URL) // validated by the config
	}
	return proxy.New(route, targets, transport)
}
