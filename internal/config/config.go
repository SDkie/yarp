// Package config parses the yarp YAML configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the root of the yarp configuration.
type Config struct {
	EntryPoints map[string]EntryPoint `yaml:"entryPoints"`
}

// EntryPoint is a named network address yarp listens on.
type EntryPoint struct {
	Address string `yaml:"address"`
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
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("parse config: file is empty")
		}
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
	return nil
}
