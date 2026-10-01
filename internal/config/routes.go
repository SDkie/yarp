package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
)

// RoutesConfig is the routing configuration loaded by the file provider.
type RoutesConfig struct {
	Routes map[string]Route `yaml:"routes"`
}

// Route forwards requests matching Host and PathPrefix, received on one of
// EntryPoints, to one of Servers. At least one of Host or PathPrefix is set.
type Route struct {
	// Host is an exact host ("example.com") or a single-level wildcard
	// ("*.example.com"). Stored in lowercase.
	Host string `yaml:"host"`
	// PathPrefix matches whole path segments: "/api" matches /api and
	// /api/users but not /apix.
	PathPrefix  string   `yaml:"pathPrefix"`
	EntryPoints []string `yaml:"entryPoints"`
	Servers     []Server `yaml:"servers"`
}

// Server is a backend that requests are forwarded to.
type Server struct {
	URL string `yaml:"url"`
}

// LoadRoutes reads and parses the routes file at path. Route entry points
// are checked against entryPoints from the main config.
func LoadRoutes(path string, entryPoints map[string]EntryPoint) (*RoutesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read routes %q: %w", path, err)
	}
	return ParseRoutes(data, entryPoints)
}

// ParseRoutes decodes YAML routes data and validates it. Unknown fields and
// duplicate keys are rejected.
func ParseRoutes(data []byte, entryPoints map[string]EntryPoint) (*RoutesConfig, error) {
	var rc RoutesConfig
	if err := decodeStrict(data, &rc); err != nil {
		return nil, fmt.Errorf("parse routes: %w", err)
	}
	if err := rc.validate(entryPoints); err != nil {
		return nil, fmt.Errorf("invalid routes: %w", err)
	}
	return &rc, nil
}

func (rc *RoutesConfig) validate(entryPoints map[string]EntryPoint) error {
	if len(rc.Routes) == 0 {
		return errors.New("routes is required and must define at least one route")
	}
	// Sorted so the reported error is the same on every run.
	for _, name := range slices.Sorted(maps.Keys(rc.Routes)) {
		r := rc.Routes[name]
		if err := r.validate(entryPoints); err != nil {
			return fmt.Errorf("routes.%s: %w", name, err)
		}
		rc.Routes[name] = r
	}
	return nil
}

// validate checks the route and normalizes Host to lowercase.
func (r *Route) validate(entryPoints map[string]EntryPoint) error {
	if r.Host == "" && r.PathPrefix == "" {
		return errors.New("one of host or pathPrefix is required")
	}
	if r.Host != "" {
		r.Host = strings.ToLower(r.Host)
		if err := validateHost(r.Host); err != nil {
			return fmt.Errorf("host %q: %w", r.Host, err)
		}
	}
	if r.PathPrefix != "" {
		if err := validatePathPrefix(r.PathPrefix); err != nil {
			return fmt.Errorf("pathPrefix %q: %w", r.PathPrefix, err)
		}
	}

	if len(r.EntryPoints) == 0 {
		return errors.New("entryPoints must list at least one entry point")
	}
	seenEP := make(map[string]bool, len(r.EntryPoints))
	for _, ep := range r.EntryPoints {
		if _, ok := entryPoints[ep]; !ok {
			return fmt.Errorf("entryPoints: unknown entry point %q", ep)
		}
		if seenEP[ep] {
			return fmt.Errorf("entryPoints: %q is listed more than once", ep)
		}
		seenEP[ep] = true
	}

	if len(r.Servers) == 0 {
		return errors.New("servers must list at least one server")
	}
	seenURL := make(map[string]bool, len(r.Servers))
	for i, s := range r.Servers {
		if err := validateServerURL(s.URL); err != nil {
			return fmt.Errorf("servers[%d].url %q: %w", i, s.URL, err)
		}
		if seenURL[s.URL] {
			return fmt.Errorf("servers[%d].url %q is listed more than once", i, s.URL)
		}
		seenURL[s.URL] = true
	}
	return nil
}

func validateHost(host string) error {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return errors.New("must not include a port")
	}
	if !strings.Contains(host, "*") {
		return nil
	}
	rest, ok := strings.CutPrefix(host, "*.")
	if !ok || rest == "" || strings.Contains(rest, "*") || strings.HasPrefix(rest, ".") {
		return errors.New(`wildcard is only allowed as a leading "*." as in "*.example.com"`)
	}
	return nil
}

func validatePathPrefix(prefix string) error {
	if !strings.HasPrefix(prefix, "/") {
		return errors.New(`must start with "/"`)
	}
	if prefix != "/" && strings.HasSuffix(prefix, "/") {
		return errors.New(`must not end with "/"`)
	}
	return nil
}

func validateServerURL(raw string) error {
	if raw == "" {
		return errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	return nil
}
