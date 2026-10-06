package proxy

import (
	"maps"
	"net/http"
	"slices"
	"testing"
)

// TestRemoveHopByHopHeaders checks that standard and Connection-listed headers are removed.
func TestRemoveHopByHopHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header http.Header
		want   http.Header
	}{
		{
			"standard hop-by-hop headers",
			http.Header{"Connection": {"close"}, "Proxy-Connection": {"x"}, "Keep-Alive": {"5"}, "Proxy-Authenticate": {"x"},
				"Proxy-Authorization": {"x"}, "Te": {"x"}, "Trailer": {"x"}, "Transfer-Encoding": {"x"}, "Upgrade": {"x"}, "X-Keep": {"yes"}},
			http.Header{"X-Keep": {"yes"}},
		},
		{
			"headers listed in Connection",
			http.Header{"Connection": {"X-A, X-B"}, "X-A": {"1"}, "X-B": {"2"}, "X-Keep": {"yes"}},
			http.Header{"X-Keep": {"yes"}},
		},
		{
			"several Connection values",
			http.Header{"Connection": {"X-A", "X-B"}, "X-A": {"1"}, "X-B": {"2"}, "X-Keep": {"yes"}},
			http.Header{"X-Keep": {"yes"}},
		},
		{
			"empty names in Connection ignored",
			http.Header{"Connection": {"X-A,, ,"}, "X-A": {"1"}, "X-Keep": {"yes"}},
			http.Header{"X-Keep": {"yes"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removeHopByHopHeaders(tt.header)
			if !maps.EqualFunc(tt.header, tt.want, slices.Equal) {
				t.Errorf("headers = %v, want %v", tt.header, tt.want)
			}
		})
	}
}

// TestHeaderValuesContains checks case-insensitive lookup in comma-separated header values.
func TestHeaderValuesContains(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{"exact", []string{"trailers"}, true},
		{"other case", []string{"Trailers"}, true},
		{"in a list with spaces", []string{"gzip,  trailers "}, true},
		{"in a later value", []string{"gzip", "trailers"}, true},
		{"absent", []string{"gzip"}, false},
		{"longer value", []string{"trailersx"}, false},
		{"no header", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{"Te": tt.values}
			if got := headerValuesContains(h, "Te", "trailers"); got != tt.want {
				t.Errorf("headerValuesContains(%q) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}
