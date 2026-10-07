package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// TestGetSignalURL checks that /v1/<signal> is added to the endpoint, keeping its base path.
func TestGetSignalURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		endpoint string
		signal   string
		want     string
	}{
		{"http://otel:4318", "traces", "http://otel:4318/v1/traces"},
		{"http://otel:4318/", "metrics", "http://otel:4318/v1/metrics"},
		{"https://otel/base", "logs", "https://otel/base/v1/logs"},
		{"https://otel/base/", "logs", "https://otel/base/v1/logs"},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			t.Parallel()
			if got := getSignalURL(tt.endpoint, tt.signal); got != tt.want {
				t.Errorf("getSignalURL(%q, %q) = %q, want %q", tt.endpoint, tt.signal, got, tt.want)
			}
		})
	}
}

// TestGetMetricReaderOptions checks that metricInterval is used unless OTEL_METRIC_EXPORT_INTERVAL is set.
func TestGetMetricReaderOptions(t *testing.T) {
	tests := []struct {
		env  string
		want int // number of options
	}{
		{"", 1},
		{"5000", 0},
	}
	for _, tt := range tests {
		t.Run("env="+tt.env, func(t *testing.T) {
			t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", tt.env)
			if got := getMetricReaderOptions(); len(got) != tt.want {
				t.Errorf("got %d options, want %d", len(got), tt.want)
			}
		})
	}
}

// TestNewResource checks the service name and version, and that OTEL_SERVICE_NAME overrides the name.
func TestNewResource(t *testing.T) {
	tests := []struct {
		env      string
		wantName string
	}{
		{"", "yarp"},
		{"edge", "edge"},
	}
	for _, tt := range tests {
		t.Run("env="+tt.env, func(t *testing.T) {
			t.Setenv("OTEL_SERVICE_NAME", tt.env)
			res, err := newResource(context.Background(), "1.2.3")
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			set := res.Set()
			for key, want := range map[attribute.Key]string{"service.name": tt.wantName, "service.version": "1.2.3"} {
				if got, _ := set.Value(key); got.AsString() != want {
					t.Errorf("%s = %q, want %q", key, got.AsString(), want)
				}
			}
		})
	}
}
