// Package proxy is a minimal HTTP reverse proxy that forwards requests to a
// set of backend servers, picked round-robin.
//
// Not supported yet: protocol upgrades (WebSocket) and forwarding 1xx
// informational responses.
package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
)

// ReverseProxy forwards each request to one of its servers, round-robin.
// The request path is appended to the server's base path, the query is
// merged with the server's query, and the original Host header is kept.
type ReverseProxy struct {
	name      string
	servers   []*url.URL
	transport http.RoundTripper
	counter   atomic.Uint64 // requests so far, for round-robin
}

// New returns a proxy for the route name that forwards to servers using
// transport. servers must not be empty.
func New(name string, servers []*url.URL, transport http.RoundTripper) *ReverseProxy {
	return &ReverseProxy{name: name, servers: servers, transport: transport}
}

// ServeHTTP forwards r to the next server, round-robin, and sends the
// server's response back, or 502 when the server cannot be reached.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := p.pick()
	outreq := outgoingRequest(r, target)
	if outreq.Body != nil {
		defer outreq.Body.Close()
	}

	// RoundTrip rather than http.Client: a proxy must pass backend
	// redirects through to the client, not follow them itself.
	resp, err := p.transport.RoundTrip(outreq)
	if err != nil {
		p.backendError(w, r, target, err)
		return
	}
	defer resp.Body.Close()
	p.writeResponse(w, r, target, resp)
}

// pick returns the next server, round-robin.
func (p *ReverseProxy) pick() *url.URL {
	return p.servers[(p.counter.Add(1)-1)%uint64(len(p.servers))]
}

// backendError answers 502 when target could not be reached. A request the
// client cancelled gets no answer, since nobody is left to read it.
func (p *ReverseProxy) backendError(w http.ResponseWriter, r *http.Request, target *url.URL, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	slog.ErrorContext(r.Context(), "proxy error", "route", p.name, "server", target.Host, "error", err)
	w.WriteHeader(http.StatusBadGateway)
}
