package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SDkie/yarp/internal/badgerdb"
	"github.com/SDkie/yarp/internal/config"
	"github.com/SDkie/yarp/internal/middlewares/httpcache"
	"github.com/SDkie/yarp/internal/middlewares/telemetry"
	"github.com/SDkie/yarp/internal/server"
)

// version is yarp's version; release builds can set it with
// -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	configPath := flag.String("config", "yarp.yml", "path to the config file")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *configPath); err != nil {
		os.Exit(1) // run has already logged it
	}
}

// run starts yarp with the config file at configPath and blocks until ctx is
// cancelled. Errors are logged before the deferred tel.Stop, so they are
// exported too.
func run(ctx context.Context, configPath string) error {
	logLevel := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return err
	}
	logLevel.Set(slog.Level(cfg.Log.Level))
	slog.Info("config loaded", "path", configPath, "entryPoints", len(cfg.EntryPoints),
		"routesPath", cfg.Providers.File.Filename, "routes", len(cfg.Routes))

	// tel stays nil when OpenTelemetry is not configured.
	var tel *telemetry.Telemetry
	if cfg.Otel != nil {
		tel, err = telemetry.Setup(cfg.Otel, version, logLevel)
		if err != nil {
			slog.Error("failed to set up OpenTelemetry", "error", err)
			return err
		}
		defer tel.Stop() // runs last, so every line before it is exported
	}

	// store stays nil when the cache is disabled. It is only set to an opened
	// store, never to a nil *badgerdb.Store.
	var store httpcache.Store
	if cfg.Cache.Enabled {
		db, err := badgerdb.Open(badgerdb.DefaultDir)
		if err != nil {
			slog.Error("failed to open cache", "error", err)
			return err
		}
		defer db.Close()
		store = db
	}
	slog.Info("cache configured", "enabled", cfg.Cache.Enabled)

	if err := serve(ctx, cfg, store, tel); err != nil {
		slog.Error("yarp failed", "error", err)
		return err
	}
	slog.Info("yarp stopped")
	return nil
}

// serve serves the entry points until ctx is cancelled. A nil store or tel
// turns that layer off.
func serve(ctx context.Context, cfg *config.Config, store httpcache.Store, tel *telemetry.Telemetry) error {
	srv, err := server.New(cfg.EntryPoints, cfg.Routes, store, tel)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	if err := srv.Listen(); err != nil {
		return err // it names the entry point and address
	}
	return srv.Serve(ctx)
}
