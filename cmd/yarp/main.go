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

	"github.com/SDkie/yarp/internal/config"
	"golang.org/x/sync/errgroup"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 180 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	configPath := flag.String("config", "yarp.yml", "path to the config file (required)")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	g, gctx := errgroup.WithContext(ctx)

	// gctx is cancelled on a shutdown signal or when any entry point fails,
	// which makes every other entry point shut down too.
	for name, ep := range cfg.EntryPoints {
		g.Go(func() error {
			return serve(gctx, name, ep)
		})
	}

	err = g.Wait()
	stop()
	if err != nil {
		slog.Error("yarp exited with error", "error", err)
		os.Exit(1)
	}
	slog.Info("yarp stopped")
}

// loadConfig loads and validates the config file at path.
func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	slog.Info("config loaded", "path", path, "entryPoints", len(cfg.EntryPoints))
	return cfg, nil
}

// serve runs an HTTP server for the named entry point until ctx is
// cancelled, then shuts it down gracefully within shutdownTimeout.
func serve(ctx context.Context, name string, ep config.EntryPoint) error {
	srv := &http.Server{
		Addr:              ep.Address,
		Handler:           entryPointHandler(name),
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

// entryPointHandler is a placeholder until routing to backends is implemented.
func entryPointHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request received",
			"entrypoint", name,
			"method", r.Method,
			"host", r.Host,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		http.Error(w, "no route configured", http.StatusNotFound)
	})
}
