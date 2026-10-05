// Package server runs yarp's entry points: one HTTP server per entry point,
// each serving the router built for it.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
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

// New builds the router of every entry point (see router.Build) and the
// HTTP server that serves it.
func New(entryPoints map[string]config.EntryPoint, routes map[string]config.Route, c *cache.Cache, tel *telemetry.Telemetry) (*Server, error) {
	handlers, err := router.Build(entryPoints, routes, c, tel)
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
	return nil
}
