package config

import (
	"net/url"
	"testing"
)

// entryPoints is the main config's entry points for the routes tests.
var entryPoints = map[string]EntryPoint{"web": {Address: ":8080"}, "admin": {Address: ":8081"}}

// routeYAML returns a routes file with one route, r1, whose fields are body.
func routeYAML(body string) string {
	return "routes:\n  r1:\n" + body
}

// TestParseRoutes checks that valid routes parse and each invalid one fails with its reason.
func TestParseRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"pathPrefix only", validRoutes, ""},
		{"host only", routeYAML("    host: example.com\n    entryPoints: [web]\n    servers:\n      - url: http://a\n"), ""},
		{"two entry points, two servers", routeYAML("    pathPrefix: /\n    entryPoints: [web, admin]\n    servers:\n      - url: http://a\n      - url: http://b\n"), ""},
		{"empty file", "", "file is empty"},
		{"no routes", "routes: {}\n", "routes is required"},
		{"unknown field", routeYAML("    pathPrefix: /\n    weight: 2\n"), "field weight not found"},
		{"no host or pathPrefix", routeYAML("    entryPoints: [web]\n"), "routes.r1: one of host or pathPrefix is required"},
		{"invalid host", routeYAML("    host: a.com:80\n"), `routes.r1: host "a.com:80": must not include a port`},
		{"invalid pathPrefix", routeYAML("    pathPrefix: api\n"), `routes.r1: pathPrefix "api": must start with "/"`},
		{"no entry points", routeYAML("    pathPrefix: /\n"), "routes.r1: entryPoints must list at least one entry point"},
		{"unknown entry point", routeYAML("    pathPrefix: /\n    entryPoints: [other]\n"), `routes.r1: entryPoints: unknown entry point "other"`},
		{"repeated entry point", routeYAML("    pathPrefix: /\n    entryPoints: [web, web]\n"), `routes.r1: entryPoints: "web" is listed more than once`},
		{"no servers", routeYAML("    pathPrefix: /\n    entryPoints: [web]\n"), "routes.r1: servers must list at least one server"},
		{"no server url", routeYAML("    pathPrefix: /\n    entryPoints: [web]\n    servers:\n      - {}\n"), "routes.r1: servers[0].url is required"},
		{"invalid server url", routeYAML("    pathPrefix: /\n    entryPoints: [web]\n    servers:\n      - url: ftp://a\n"), `routes.r1: servers[0].url "ftp://a": scheme must be http or https`},
		{"unparsable server url", routeYAML("    pathPrefix: /\n    entryPoints: [web]\n    servers:\n      - url: \"%zz\"\n"), `invalid URL escape "%zz"`},
		{"repeated server url", routeYAML("    pathPrefix: /\n    entryPoints: [web]\n    servers:\n      - url: http://a\n      - url: http://a\n"), `routes.r1: servers[1].url "http://a" is listed more than once`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseRoutes([]byte(tt.yaml), entryPoints)
			if !errMatches(err, tt.wantErr) {
				t.Errorf("parseRoutes error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestParseRoutesValues checks that a route keeps its host as written and gets its server URLs parsed.
func TestParseRoutesValues(t *testing.T) {
	t.Parallel()
	yaml := routeYAML("    host: API.Example.com\n    pathPrefix: /api\n    entryPoints: [web]\n    servers:\n      - url: http://127.0.0.1:9000/base\n")
	routes, err := parseRoutes([]byte(yaml), entryPoints)
	if err != nil {
		t.Fatalf("parseRoutes: %v", err)
	}
	r := routes["r1"]
	if r.Host != "API.Example.com" || r.PathPrefix != "/api" {
		t.Errorf("route = {Host %q, PathPrefix %q}, want {%q, %q}", r.Host, r.PathPrefix, "API.Example.com", "/api")
	}
	if len(r.Servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(r.Servers))
	}
	if u := r.Servers[0].URL; u.Host != "127.0.0.1:9000" || u.Path != "/base" {
		t.Errorf("server URL = {Host %q, Path %q}, want {%q, %q}", u.Host, u.Path, "127.0.0.1:9000", "/base")
	}
}

// TestValidateHost checks exact and one-level wildcard hosts, and rejects ports and misplaced wildcards.
func TestValidateHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host    string
		wantErr string
	}{
		{"example.com", ""},
		{"*.example.com", ""},
		{"example.com:80", "must not include a port"},
		{"*example.com", "wildcard is only allowed"},
		{"a.*.com", "wildcard is only allowed"},
		{"*.", "wildcard is only allowed"},
		{"*.*.com", "wildcard is only allowed"},
		{"*..com", "wildcard is only allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if err := validateHost(tt.host); !errMatches(err, tt.wantErr) {
				t.Errorf("validateHost(%q) = %v, want %q", tt.host, err, tt.wantErr)
			}
		})
	}
}

// TestValidatePathPrefix checks that a pathPrefix starts with "/" and, unless it is "/", does not end with one.
func TestValidatePathPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		prefix  string
		wantErr string
	}{
		{"/", ""},
		{"/api", ""},
		{"/api/v1", ""},
		{"api", `must start with "/"`},
		{"/api/", `must not end with "/"`},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			t.Parallel()
			if err := validatePathPrefix(tt.prefix); !errMatches(err, tt.wantErr) {
				t.Errorf("validatePathPrefix(%q) = %v, want %q", tt.prefix, err, tt.wantErr)
			}
		})
	}
}

// TestValidateServerURL checks that a server URL is http or https and has a host.
func TestValidateServerURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url     string
		wantErr string
	}{
		{"http://a", ""},
		{"https://a:8443/x", ""},
		{"ftp://a", "scheme must be http or https"},
		{"/path", "scheme must be http or https"},
		{"http://", "must include a host"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			if err := validateServerURL(u); !errMatches(err, tt.wantErr) {
				t.Errorf("validateServerURL(%q) = %v, want %q", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestURLUnmarshalText checks that empty text leaves the URL nil and other text is parsed.
func TestURLUnmarshalText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		want    string // the parsed URL; "" means nil
		wantErr string
	}{
		{"empty", "", "", ""},
		{"valid", "http://a:1/x", "http://a:1/x", ""},
		{"invalid escape", "%zz", "", `invalid URL escape "%zz"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var u URL
			err := u.UnmarshalText([]byte(tt.text))
			if !errMatches(err, tt.wantErr) {
				t.Fatalf("UnmarshalText error = %v, want %q", err, tt.wantErr)
			}
			switch {
			case tt.want == "" && u.URL != nil:
				t.Errorf("URL = %q, want nil", u.URL)
			case tt.want != "" && (u.URL == nil || u.String() != tt.want):
				t.Errorf("URL = %v, want %q", u.URL, tt.want)
			}
		})
	}
}
