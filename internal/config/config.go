// Package config parses the yarp YAML configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the root of the yarp configuration.
type Config struct {
	EntryPoints map[string]EntryPoint `yaml:"entryPoints"`
	Providers   Providers             `yaml:"providers"`
	Cache       Cache                 `yaml:"cache"`
	Log         Log                   `yaml:"log"`
	// Otel is nil when OpenTelemetry is not configured.
	Otel *Otel `yaml:"otel"`
}

// EntryPoint is a named network address yarp listens on.
type EntryPoint struct {
	Address string `yaml:"address"`
}

// Providers lists the sources of the routing configuration.
// At least one provider must be configured.
type Providers struct {
	File *FileProvider `yaml:"file"`
}

// Cache configures the HTTP response cache.
type Cache struct {
	// Enabled turns the cache on for every route. It defaults to true.
	Enabled bool `yaml:"enabled"`
}

// Log configures yarp's logs.
type Log struct {
	// Level is DEBUG, INFO, WARN or ERROR, in any case. It defaults to ERROR.
	Level string `yaml:"level"`
}

// GetLevel returns the slog level named by l.Level.
func (l Log) GetLevel() (slog.Level, error) {
	switch strings.ToUpper(l.Level) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("log.level %q is invalid, expected DEBUG, INFO, WARN or ERROR", l.Level)
}

// Otel configures sending telemetry to an OpenTelemetry collector. Other
// settings come from the standard OTEL_* environment variables.
type Otel struct {
	// Endpoint is the collector's OTLP/HTTP base URL, e.g. "http://localhost:4318".
	Endpoint string `yaml:"endpoint"`
}

// FileProvider loads the routing configuration from a file.
type FileProvider struct {
	Filename string `yaml:"filename"`
}

// Load reads and parses the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes YAML config data and validates it. Unknown fields and
// duplicate keys are rejected.
func Parse(data []byte) (*Config, error) {
	// Defaults for omitted fields.
	cfg := Config{
		Cache: Cache{Enabled: true},
		Log:   Log{Level: "ERROR"},
	}
	if err := decodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if len(c.EntryPoints) == 0 {
		return errors.New("entryPoints is required and must define at least one entry point")
	}
	for name, ep := range c.EntryPoints {
		if ep.Address == "" {
			return fmt.Errorf("entryPoints.%s.address is required", name)
		}
		_, port, err := net.SplitHostPort(ep.Address)
		if err != nil {
			return fmt.Errorf("entryPoints.%s.address %q is invalid, expected [host]:port: %w", name, ep.Address, err)
		}
		if port == "" {
			return fmt.Errorf("entryPoints.%s.address %q is missing a port", name, ep.Address)
		}
	}

	if c.Providers.File == nil {
		return errors.New("providers is required")
	}
	if c.Providers.File.Filename == "" {
		return errors.New("providers.file.filename is required")
	}

	if _, err := c.Log.GetLevel(); err != nil {
		return err
	}

	if c.Otel != nil {
		if c.Otel.Endpoint == "" {
			return errors.New("otel.endpoint is required")
		}
		u, err := url.Parse(c.Otel.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("otel.endpoint %q is invalid, expected an http or https URL such as http://localhost:4318", c.Otel.Endpoint)
		}
	}
	return nil
}

// decodeStrict decodes YAML data into out, rejecting empty input, unknown
// fields and duplicate keys.
func decodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("file is empty")
		}
		return err
	}
	return nil
}
