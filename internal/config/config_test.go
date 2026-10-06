package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	entryPointsYAML = `entryPoints:
  web:
    address: ":8080"
`
	providersYAML = `providers:
  file:
    filename: routes.yml
`
	validConfig = entryPointsYAML + providersYAML

	validRoutes = `routes:
  api:
    pathPrefix: /api
    entryPoints: [web]
    servers:
      - url: http://127.0.0.1:9000
`
)

// errMatches reports whether err is nil when want is "", or contains want
// otherwise.
func errMatches(err error, want string) bool {
	if want == "" {
		return err == nil
	}
	return err != nil && strings.Contains(err.Error(), want)
}

// writeFile writes content to name in dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestLoad checks that Load reads the config file, then the routes file, finding a relative one next to the config file.
func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// config and routes are written to {dir}/yarp.yml and {dir}/routes.yml,
		// where {dir} is a temporary directory; "" writes no file.
		config       string
		routes       string
		wantFilename string
		wantErr      string
	}{
		{"relative routes path", validConfig, validRoutes, "{dir}/routes.yml", ""},
		{"absolute routes path", entryPointsYAML + "providers:\n  file:\n    filename: {dir}/routes.yml\n", validRoutes, "{dir}/routes.yml", ""},
		{"missing config file", "", validRoutes, "", "read config"},
		{"invalid config file", providersYAML, validRoutes, "", "invalid config: entryPoints is required"},
		{"missing routes file", validConfig, "", "", "file provider: read routes"},
		{"invalid routes file", validConfig, "routes: {}\n", "", "file provider: invalid routes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range map[string]string{"yarp.yml": tt.config, "routes.yml": tt.routes} {
				if content != "" {
					writeFile(t, dir, name, strings.ReplaceAll(content, "{dir}", dir))
				}
			}

			cfg, err := Load(filepath.Join(dir, "yarp.yml"))
			if !errMatches(err, tt.wantErr) {
				t.Fatalf("Load error = %v, want %q", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if want := strings.ReplaceAll(tt.wantFilename, "{dir}", dir); cfg.Providers.File.Filename != want {
				t.Errorf("Filename = %q, want %q", cfg.Providers.File.Filename, want)
			}
			if _, ok := cfg.Routes["api"]; !ok || len(cfg.Routes) != 1 {
				t.Errorf("Routes = %v, want only route api", cfg.Routes)
			}
		})
	}
}

// minimalConfig returns what validConfig parses to: its entry point and
// provider, with every other section at its default.
func minimalConfig() *Config {
	return &Config{
		EntryPoints: map[string]EntryPoint{"web": {Address: ":8080"}},
		Providers:   Providers{File: &FileProvider{Filename: "routes.yml"}},
		Cache:       Cache{Enabled: true},
		Log:         Log{Level: Level(slog.LevelError)},
	}
}

// TestParse checks what valid configs parse to, defaults included, and that each invalid one fails with its reason.
func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		want    *Config // nil when wantErr is set
		wantErr string
	}{
		{"omitted sections get defaults", validConfig, minimalConfig(), ""},
		{"empty sections get defaults", validConfig + "cache: {}\nlog: {}\n", minimalConfig(), ""},
		{"null sections get defaults", validConfig + "cache:\nlog:\n", minimalConfig(), ""},
		{"all sections set", validConfig + "cache:\n  enabled: false\nlog:\n  level: info\notel:\n  endpoint: https://otel:4318\n", &Config{
			EntryPoints: map[string]EntryPoint{"web": {Address: ":8080"}},
			Providers:   Providers{File: &FileProvider{Filename: "routes.yml"}},
			Cache:       Cache{Enabled: false},
			Log:         Log{Level: Level(slog.LevelInfo)},
			Otel:        &Otel{Endpoint: "https://otel:4318"},
		}, ""},
		{"empty file", "", nil, "file is empty"},
		{"unknown field", validConfig + "extra: 1\n", nil, "field extra not found"},
		{"routes in config file", validConfig + "routes: {}\n", nil, "field routes not found"},
		{"duplicate key", validConfig + providersYAML, nil, `"providers" already defined`},
		{"wrong type", validConfig + "cache:\n  enabled: maybe\n", nil, "cannot unmarshal"},
		{"invalid log level", validConfig + "log:\n  level: verbose\n", nil, `log.level "verbose" is invalid`},
		{"no entry points", providersYAML, nil, "entryPoints is required"},
		{"no address", "entryPoints:\n  web: {}\n" + providersYAML, nil, "entryPoints.web.address is required"},
		{"address without port", "entryPoints:\n  web:\n    address: localhost\n" + providersYAML, nil, `entryPoints.web.address "localhost" is invalid`},
		{"address with empty port", "entryPoints:\n  web:\n    address: \"localhost:\"\n" + providersYAML, nil, `entryPoints.web.address "localhost:" is missing a port`},
		{"no providers", entryPointsYAML, nil, "providers is required"},
		{"no filename", entryPointsYAML + "providers:\n  file: {}\n", nil, "providers.file.filename is required"},
		{"otel without endpoint", validConfig + "otel: {}\n", nil, "otel.endpoint is required"},
		{"otel endpoint not http", validConfig + "otel:\n  endpoint: ftp://otel\n", nil, `otel.endpoint "ftp://otel" is invalid`},
		{"otel endpoint without host", validConfig + "otel:\n  endpoint: \"http://\"\n", nil, `otel.endpoint "http://" is invalid`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parse([]byte(tt.yaml))
			if !errMatches(err, tt.wantErr) {
				t.Fatalf("parse error = %v, want %q", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parse = %s, want %s", describe(got), describe(tt.want))
			}
		})
	}
}

// describe formats cfg with the values behind its pointers, which %+v
// would print as addresses.
func describe(cfg *Config) string {
	return fmt.Sprintf("{EntryPoints:%v File:%+v Cache:%+v Log:%v Otel:%+v}",
		cfg.EntryPoints, *cfg.Providers.File, cfg.Cache, slog.Level(cfg.Log.Level), cfg.Otel)
}

// TestParseEntryPointErrorOrder checks that with several bad entry points, the first by name is reported.
func TestParseEntryPointErrorOrder(t *testing.T) {
	t.Parallel()
	yaml := "entryPoints:\n  b: {}\n  a: {}\n  c: {}\n" + providersYAML
	// Map order is random, so one run could pass by chance.
	for range 20 {
		_, err := parse([]byte(yaml))
		if want := "entryPoints.a.address is required"; !errMatches(err, want) {
			t.Fatalf("parse error = %v, want %q", err, want)
		}
	}
}

// TestLevelUnmarshalText checks that the four level names are read in any case, and nothing else.
func TestLevelUnmarshalText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text    string
		want    slog.Level
		wantErr string
	}{
		{"debug", slog.LevelDebug, ""},
		{"Info", slog.LevelInfo, ""},
		{"WARN", slog.LevelWarn, ""},
		{"error", slog.LevelError, ""},
		{"verbose", 0, `log.level "verbose" is invalid`},
		{"info+2", 0, `log.level "info+2" is invalid`},
		{"", 0, `log.level "" is invalid`},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			var l Level
			err := l.UnmarshalText([]byte(tt.text))
			if !errMatches(err, tt.wantErr) {
				t.Fatalf("UnmarshalText error = %v, want %q", err, tt.wantErr)
			}
			if err == nil && slog.Level(l) != tt.want {
				t.Errorf("level = %v, want %v", slog.Level(l), tt.want)
			}
		})
	}
}
