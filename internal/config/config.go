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
	Providers   Providers             `yaml:"providers"`
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
	var cfg Config
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
