package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// testTelemetry is a Telemetry that records to memory.
type testTelemetry struct {
	*Telemetry
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// newTestTelemetry returns a Telemetry whose spans and metrics can be read
// as soon as they are recorded.
func newTestTelemetry(t *testing.T) *testTelemetry {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	tel, err := New(
		sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdklog.NewLoggerProvider(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testTelemetry{Telemetry: tel, spans: spans, reader: reader}
}

// span returns the only ended span.
func (tel *testTelemetry) span(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := tel.spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d ended spans, want 1", len(ended))
	}
	return ended[0]
}

// point is one data point of a metric: its attributes and its value, a
// counter's sum or a histogram's count.
type point struct {
	attrs map[string]string
	value int64
}

// points returns the data points of the metric called name; none if nothing
// was recorded to it.
func (tel *testTelemetry) points(t *testing.T, name string) []point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := tel.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var points []point
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					points = append(points, point{attrMap(dp.Attributes.ToSlice()), dp.Value})
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					points = append(points, point{attrMap(dp.Attributes.ToSlice()), int64(dp.Count)})
				}
			default:
				t.Fatalf("metric %s has unexpected type %T", name, m.Data)
			}
		}
	}
	return points
}

// attrMap returns kvs as key → value strings, for comparing.
func attrMap(kvs []attribute.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value.String()
	}
	return m
}

// setDefaultLogger makes l the default logger until the test ends.
func setDefaultLogger(t *testing.T, l *slog.Logger) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(l)
	t.Cleanup(func() { slog.SetDefault(old) })
}

// TestStopNil checks that Stop on a nil Telemetry, which means telemetry is off, does nothing.
func TestStopNil(t *testing.T) {
	t.Parallel()
	var tel *Telemetry
	tel.Stop()
}

// TestStop checks that Stop restores the console logger and shuts the providers down, logging when that fails.
func TestStop(t *testing.T) {
	setDefaultLogger(t, slog.Default())
	tel := newTestTelemetry(t)
	var console bytes.Buffer
	tel.console = slog.New(slog.NewTextHandler(&console, nil))

	tel.Stop()
	if slog.Default() != tel.console {
		t.Error("default logger was not restored to the console logger")
	}
	_, span := tel.tracer.Start(context.Background(), "after stop")
	span.End()
	if n := len(tel.spans.Ended()); n != 0 {
		t.Errorf("got %d spans after Stop, want 0", n)
	}
	var rm metricdata.ResourceMetrics
	if err := tel.reader.Collect(context.Background(), &rm); !errors.Is(err, sdkmetric.ErrReaderShutdown) {
		t.Errorf("collect after Stop = %v, want %v", err, sdkmetric.ErrReaderShutdown)
	}
	if console.Len() != 0 {
		t.Errorf("first Stop logged %q, want nothing", console.String())
	}

	// The providers are already shut down, so a second Stop fails.
	tel.Stop()
	if !strings.Contains(console.String(), "failed to stop OpenTelemetry") {
		t.Errorf("second Stop logged %q, want the shutdown failure", console.String())
	}
}
