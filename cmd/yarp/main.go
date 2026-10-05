package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SDkie/yarp/internal/cache"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
	"github.com/SDkie/yarp/internal/server"
)

// version is yarp's version; release builds can set it with
// -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	// run has already logged the error.
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// run starts yarp and blocks until it stops. Errors are logged where they
// happen, before the deferred tel.Stop, so they are exported too.
func run() error {
	logLevel := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	configPath := flag.String("config", "yarp.yml", "path to the config file")
	flag.Parse()

	cfg, routes, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return err
	}
	level, _ := cfg.Log.GetLevel() // validated by loadConfig
	logLevel.Set(level)
	slog.Info("config loaded", "path", *configPath, "entryPoints", len(cfg.EntryPoints),
		"routesPath", cfg.Providers.File.Filename, "routes", len(routes.Routes))

	tel, err := telemetry.Setup(cfg.Otel, version, logLevel)
	if err != nil {
		slog.Error("failed to set up OpenTelemetry", "error", err)
		return err
	}
	defer tel.Stop() // runs last, so every line before it is exported

	// c stays nil when the cache is disabled.
	var c *cache.Cache
	if cfg.Cache.Enabled {
		c, err = cache.Open(cache.DefaultDir)
		if err != nil {
			slog.Error("failed to open cache", "error", err)
			return err
		}
		defer closeCache(c)
	}
	slog.Info("cache configured", "enabled", cfg.Cache.Enabled)

	srv, err := server.New(cfg.EntryPoints, routes.Routes, c, tel)
	if err != nil {
		slog.Error("failed to build routers", "error", err)
		return err
	}
	if err := srv.Listen(); err != nil {
		slog.Error("failed to listen", "error", err)
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		slog.Error("yarp exited with error", "error", err)
		return err
	}
	slog.Info("yarp stopped")
	return nil
}

// closeCache flushes and closes the cache.
func closeCache(c *cache.Cache) {
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
	routes, err := config.LoadRoutes(cfg.Providers.File.Filename, cfg.EntryPoints)
	if err != nil {
		return nil, nil, fmt.Errorf("file provider: %w", err)
	}
	return cfg, routes, nil
}
