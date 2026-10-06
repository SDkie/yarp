package httpcache

import (
	"maps"
	"net/http"
	"testing"
)

// TestParseCacheControl checks how Cache-Control lines are parsed into directives.
func TestParseCacheControl(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		lines []string
		want  cacheControl
	}{
		{"none", nil, cacheControl{}},
		{"names are lower-cased", []string{"Max-Age=60, NO-STORE"}, cacheControl{"max-age": "60", "no-store": ""}},
		{"quotes are removed", []string{`max-age="60"`}, cacheControl{"max-age": "60"}},
		{"spaces are trimmed", []string{" max-age = 60 ,  public "}, cacheControl{"max-age": "60", "public": ""}},
		{"first occurrence wins", []string{"max-age=60, max-age=10"}, cacheControl{"max-age": "60"}},
		{"several lines", []string{"max-age=60", "public"}, cacheControl{"max-age": "60", "public": ""}},
		{"empty directives are skipped", []string{", ,max-age=60,"}, cacheControl{"max-age": "60"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{"Cache-Control": tt.lines}
			if got := parseCacheControl(h); !maps.Equal(got, tt.want) {
				t.Errorf("parseCacheControl(%q) = %v, want %v", tt.lines, got, tt.want)
			}
		})
	}
}

// TestCacheControlHas checks that has reports whether a directive is present.
func TestCacheControlHas(t *testing.T) {
	t.Parallel()
	cc := cacheControl{"public": "", "max-age": "60"}
	tests := []struct {
		name      string
		directive string
		want      bool
	}{
		{"present without a value", "public", true},
		{"present with a value", "max-age", true},
		{"absent", "private", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := cc.has(tt.directive); got != tt.want {
				t.Errorf("has(%q) = %v, want %v", tt.directive, got, tt.want)
			}
		})
	}
}
