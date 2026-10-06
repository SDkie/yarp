// Package server runs yarp's entry points: one HTTP server per entry point,
// each serving the handler chain built for it (see newHandlers).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/httpcache"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
	"github.com/SDkie/yarp/internal/proxy"
	"github.com/SDkie/yarp/internal/router"
	"golang.org/x/sync/errgroup"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 180 * time.Second
)

// Server serves yarp's entry points. Call Listen, then Serve.
type Server struct {
	// entryPoints is sorted by name, so they are opened in the same order
	// on every run.
	entryPoints []*entryPoint
}

type entryPoint struct {
	name     string
	address  string // as configured, e.g. ":8080"
	srv      *http.Server
	listener net.Listener // set by Listen
}

// New builds the handler of every entry point (see newHandlers) and the
// HTTP server that serves it. A nil cache or tel turns that layer off.
func New(entryPoints map[string]config.EntryPoint, routes map[string]config.Route, cache *cache.Cache, tel *telemetry.Telemetry) (*Server, error) {
	// A nil *cache.Cache must become a nil Store, not a Store holding nil.
	var store httpcache.Store
	if cache != nil {
		store = cache
	}
	handlers, err := newHandlers(entryPoints, routes, newTransport(), store, tel)
	if err != nil {
		return nil, err
	}
	s := &Server{}
	for _, name := range slices.Sorted(maps.Keys(entryPoints)) {
		s.entryPoints = append(s.entryPoints, &entryPoint{
			name:    name,
			address: entryPoints[name].Address,
			srv: &http.Server{
				Handler:           handlers[name],
				ReadHeaderTimeout: readHeaderTimeout,
				IdleTimeout:       idleTimeout,
				// The server's own errors, such as handler panics, go through slog too.
				ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
			},
		})
	}
	return s, nil
}

// newHandlers returns the handler of every entry point. A request flows
// through: telemetry handler, router, cache, proxy, telemetry transport, base.
func newHandlers(
	entryPoints map[string]config.EntryPoint,
	routes map[string]config.Route,
	base http.RoundTripper,
	store httpcache.Store,
	tel *telemetry.Telemetry,
) (map[string]http.Handler, error) {
	byEntryPoint := make(map[string][]router.Route, len(entryPoints))
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		cfg := routes[name]

		transport := base
		if tel != nil {
			transport = tel.Transport(name, transport)
		}
		servers, err := serverURLs(cfg.Servers)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", name, err)
		}
		var h http.Handler = proxy.New(name, servers, transport)
		// Outside the proxy, so a hit never reaches the backend.
		if store != nil {
			h = httpcache.Handler(store, h)
		}

		// Built once, so a route on two entry points shares one round-robin.
		r := router.Route{Name: name, Host: cfg.Host, PathPrefix: cfg.PathPrefix, Handler: h}
		for _, ep := range cfg.EntryPoints {
			byEntryPoint[ep] = append(byEntryPoint[ep], r)
		}
	}

	handlers := make(map[string]http.Handler, len(entryPoints))
	for _, ep := range slices.Sorted(maps.Keys(entryPoints)) {
		rt, err := router.New(ep, byEntryPoint[ep])
		if err != nil {
			return nil, err
		}
		var h http.Handler = rt
		// Outside the router, so 404s are measured and SetRoute has a target.
		if tel != nil {
			h = tel.Handler(ep, h)
		}
		handlers[ep] = h
	}
	return handlers, nil
}

// serverURLs parses the URLs of servers.
func serverURLs(servers []config.Server) ([]*url.URL, error) {
	urls := make([]*url.URL, len(servers))
	for i, s := range servers {
		u, err := url.Parse(s.URL) // its error names the URL
		if err != nil {
			return nil, err
		}
		urls[i] = u
	}
	return urls, nil
}

// Listen opens the address of every entry point, so none serves unless all
// can. On failure, the ones already opened are closed.
func (s *Server) Listen() error {
	for i, ep := range s.entryPoints {
		ln, err := net.Listen("tcp", ep.address)
		if err != nil {
			for _, opened := range s.entryPoints[:i] {
				opened.listener.Close()
				opened.listener = nil
			}
			return fmt.Errorf("entrypoint %q: %w", ep.name, err)
		}
		ep.listener = ln
	}
	return nil
}

// Addr returns the address the named entry point listens on, such as
// "127.0.0.1:54321" for a configured "127.0.0.1:0". It is "" before Listen
// or for an unknown name.
func (s *Server) Addr(name string) string {
	for _, ep := range s.entryPoints {
		if ep.name == name && ep.listener != nil {
			return ep.listener.Addr().String()
		}
	}
	return ""
}

// Serve serves every entry point until ctx is cancelled or one of them
// fails, then shuts them all down gracefully within shutdownTimeout. Listen
// must have succeeded first.
func (s *Server) Serve(ctx context.Context) error {
	for _, ep := range s.entryPoints {
		if ep.listener == nil {
			return fmt.Errorf("entrypoint %q: Serve called before Listen", ep.name)
		}
	}

	// gctx is cancelled when ctx is or when any entry point fails, which
	// makes every other entry point shut down too.
	g, gctx := errgroup.WithContext(ctx)
	for _, ep := range s.entryPoints {
		g.Go(func() error {
			return ep.serve(gctx)
		})
	}
	return g.Wait()
}

// serve handles requests on the entry point's listener until ctx is
// cancelled, then shuts it down gracefully within shutdownTimeout.
func (ep *entryPoint) serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("entrypoint starting", "name", ep.name, "address", ep.listener.Addr().String())
		errCh <- ep.srv.Serve(ep.listener)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("entrypoint %q: %w", ep.name, err)
	case <-ctx.Done():
	}

	slog.Info("entrypoint shutting down", "name", ep.name, "reason", context.Cause(ctx))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := ep.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("entrypoint %q: shutdown: %w", ep.name, err)
	}
	// Wait for Serve to return: if it had not started when Shutdown ran, the
	// listener is only closed then.
	<-errCh
	return nil
}
