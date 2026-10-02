package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"github.com/SDkie/yarp/internal/config"
)

// newProxy returns a handler that forwards requests to servers, picking
// them round-robin. The request path is forwarded unchanged and the
// original Host header is kept.
func newProxy(route string, servers []config.Server) (http.Handler, error) {
	targets := make([]*url.URL, len(servers))
	for i, s := range servers {
		u, err := url.Parse(s.URL)
		if err != nil {
			return nil, fmt.Errorf("route %q: server %q: %w", route, s.URL, err)
		}
		targets[i] = u
	}

	var next atomic.Uint64
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := targets[(next.Add(1)-1)%uint64(len(targets))]
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxy error", "route", route, "server", r.URL.Host, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}, nil
}
