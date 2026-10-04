package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/router"
	"github.com/SDkie/yarp/internal/telemetry"
	"golang.org/x/sync/errgroup"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 180 * time.Second
)

// version is yarp's version; release builds can set it with
// -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	configPath := flag.String("config", "yarp.yml", "path to the config file")
	flag.Parse()

	cfg, routes, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	stopTelemetry, err := telemetry.Setup(cfg.Otel, version)
	if err != nil {
		slog.Error("failed to set up OpenTelemetry", "error", err)
		os.Exit(1)
	}

	// c stays nil when the cache is disabled.
	var c *cache.Cache
	if cfg.Cache.Enabled {
		c, err = cache.Open()
		if err != nil {
			slog.Error("failed to open cache", "error", err)
			stopTelemetry()
			os.Exit(1)
		}
	}
	slog.Info("cache configured", "enabled", cfg.Cache.Enabled)

	handlers, err := router.Build(cfg.EntryPoints, routes.Routes, c)
	if err != nil {
		slog.Error("failed to build routers", "error", err)
		closeCache(c)
		stopTelemetry()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	g, gctx := errgroup.WithContext(ctx)

	// gctx is cancelled on a shutdown signal or when any entry point fails,
	// which makes every other entry point shut down too.
	for name, ep := range cfg.EntryPoints {
		g.Go(func() error {
			return serve(gctx, name, ep, handlers[name])
		})
	}

	err = g.Wait()
	stop()
	closeCache(c)
	stopTelemetry()
	if err != nil {
		slog.Error("yarp exited with error", "error", err)
		os.Exit(1)
	}
	slog.Info("yarp stopped")
}

// closeCache flushes and closes the cache. It is called explicitly rather
// than deferred, because os.Exit skips deferred calls.
func closeCache(c *cache.Cache) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		slog.Error("failed to close cache", "error", err)
	}
}

// loadConfig loads and validates the config file at path, then the routes
// from the configured provider.
func loadConfig(path string) (*config.Config, *config.RoutesConfig, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("config loaded", "path", path, "entryPoints", len(cfg.EntryPoints))

	fp := cfg.Providers.File
	routes, err := config.LoadRoutes(fp.Filename, cfg.EntryPoints)
	if err != nil {
		return nil, nil, fmt.Errorf("file provider: %w", err)
	}
	slog.Info("routes loaded", "provider", "file", "path", fp.Filename, "routes", len(routes.Routes))
	return cfg, routes, nil
}

// serve runs an HTTP server for the named entry point, handling requests
// with handler, until ctx is cancelled, then shuts it down gracefully within
// shutdownTimeout.
func serve(ctx context.Context, name string, ep config.EntryPoint, handler http.Handler) error {
	srv := &http.Server{
		Addr:              ep.Address,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("entrypoint starting", "name", name, "address", ep.Address)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("entrypoint %q: %w", name, err)
	case <-ctx.Done():
	}

	slog.Info("entrypoint shutting down", "name", name, "reason", context.Cause(ctx))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("entrypoint %q: shutdown: %w", name, err)
	}
	return nil
}
