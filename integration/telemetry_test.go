package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/SDkie/yarp/internal/middlewares/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recorder is a Telemetry that records spans and metrics to memory.
type recorder struct {
	tel    *telemetry.Telemetry
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

// newRecorder returns a recorder, stopped when the test ends.
func newRecorder(t *testing.T) *recorder {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	tel, err := telemetry.New(
		sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdklog.NewLoggerProvider(),
	)
	if err != nil {
		t.Fatalf("telemetry.New: %v", err)
	}
	t.Cleanup(tel.Stop)
	return &recorder{tel: tel, spans: spans, reader: reader}
}

// spansOf returns the ended spans of kind, in the order they ended.
func (rec *recorder) spansOf(kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var spans []sdktrace.ReadOnlySpan
	for _, s := range rec.spans.Ended() {
		if s.SpanKind() == kind {
			spans = append(spans, s)
		}
	}
	return spans
}

// onlySpan returns the only ended span of kind.
func (rec *recorder) onlySpan(t *testing.T, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := rec.spansOf(kind)
	if len(spans) != 1 {
		t.Fatalf("got %d %s spans, want 1", len(spans), kind)
	}
	return spans[0]
}

// counts returns, for the metric called name, each data point's count (a
// counter's sum or a histogram's count) keyed by the value of attribute key.
func (rec *recorder) counts(t *testing.T, name string, key attribute.Key) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := rec.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	counts := make(map[string]int64)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					v, _ := dp.Attributes.Value(key)
					counts[v.String()] += dp.Value
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					v, _ := dp.Attributes.Value(key)
					counts[v.String()] += int64(dp.Count)
				}
			default:
				t.Fatalf("metric %s has unexpected type %T", name, m.Data)
			}
		}
	}
	return counts
}

// gauge returns the value of the gauge called name, which has one data point.
func (rec *recorder) gauge(t *testing.T, name string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := rec.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Gauge[int64])
			if !ok || len(data.DataPoints) != 1 {
				t.Fatalf("metric %s is %T, want a gauge with one data point", name, m.Data)
			}
			return data.DataPoints[0].Value
		}
	}
	t.Fatalf("metric %s was not recorded", name)
	return 0
}

// checkAttrs checks that span has the attributes in want.
func checkAttrs(t *testing.T, span sdktrace.ReadOnlySpan, want map[attribute.Key]string) {
	t.Helper()
	got := make(map[attribute.Key]string)
	for _, kv := range span.Attributes() {
		got[kv.Key] = kv.Value.String()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s span: %s = %q, want %q", span.SpanKind(), k, got[k], v)
		}
	}
}

func checkCounts(t *testing.T, name string, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", name, got, want)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
	}
}

// Metric names, from the OpenTelemetry semantic conventions and yarp's own.
const (
	serverDuration = "http.server.request.duration"
	clientDuration = "http.client.request.duration"
	cacheRequests  = "yarp.cache.requests"
	cacheEnabled   = "yarp.cache.enabled"
)

// Attribute keys checked below.
const (
	routeKey       = attribute.Key("yarp.route")
	entryPointKey  = attribute.Key("yarp.entrypoint")
	cacheResultKey = attribute.Key("yarp.cache.result")
	statusKey      = attribute.Key("http.response.status_code")
)

// TestTelemetryRequest checks the spans and metrics of a request proxied to
// a backend, and that the backend gets the trace context.
func TestTelemetryRequest(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	backendURL, reqs := newCaptureBackend(t)
	store := openStore(t, t.TempDir())
	y := startYarpWith(t, cacheConfig, appRoutes(backendURL), store, rec.tel)

	do(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""))
	traceparent := receive(t, reqs).req.Header.Get("Traceparent")
	y.stop(t) // Every span and metric is recorded once the request has finished.

	server := rec.onlySpan(t, trace.SpanKindServer)
	client := rec.onlySpan(t, trace.SpanKindClient)
	checkAttrs(t, server, map[attribute.Key]string{
		entryPointKey:  "web",
		routeKey:       "app",
		cacheResultKey: "miss",
		statusKey:      "200",
	})
	checkAttrs(t, client, map[attribute.Key]string{
		routeKey:  "app",
		statusKey: "200",
	})
	if client.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("client span's parent = %s, want the server span %s", client.Parent().SpanID(), server.SpanContext().SpanID())
	}
	sc := client.SpanContext()
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; traceparent != want {
		t.Errorf("backend got traceparent %q, want %q", traceparent, want)
	}

	checkCounts(t, serverDuration, rec.counts(t, serverDuration, routeKey), map[string]int64{"app": 1})
	checkCounts(t, clientDuration, rec.counts(t, clientDuration, routeKey), map[string]int64{"app": 1})
	checkCounts(t, cacheRequests, rec.counts(t, cacheRequests, cacheResultKey), map[string]int64{"miss": 1})
	if got := rec.gauge(t, cacheEnabled); got != 1 {
		t.Errorf("%s = %d, want 1 with the cache on", cacheEnabled, got)
	}
}

// TestTelemetryCacheHit checks that a cache hit is recorded as such and,
// since it never reaches the backend, has no client span.
func TestTelemetryCacheHit(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	backendURL, _ := newCountingBackend(t, cacheable("v1"))
	store := openStore(t, t.TempDir())
	y := startYarpWith(t, cacheConfig, appRoutes(backendURL), store, rec.tel)

	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheMiss, "v1")
	store.waitSets(t, setsPerResponse)
	fetch(t, newRequest(t, http.MethodGet, y.url("web", "/page"), ""), cacheHit, "v1")
	y.stop(t)

	servers := rec.spansOf(trace.SpanKindServer)
	if len(servers) != 2 {
		t.Fatalf("got %d server spans, want 2", len(servers))
	}
	checkAttrs(t, servers[1], map[attribute.Key]string{cacheResultKey: "hit"})
	if n := len(rec.spansOf(trace.SpanKindClient)); n != 1 {
		t.Errorf("got %d client spans, want 1, for the miss only", n)
	}
	checkCounts(t, cacheRequests, rec.counts(t, cacheRequests, cacheResultKey), map[string]int64{"miss": 1, "hit": 1})
}

// TestTelemetryNoRoute checks that a request matching no route is recorded
// with a 404 and no route.
func TestTelemetryNoRoute(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	routes := `
routes:
  app:
    pathPrefix: /app
    entryPoints: [web]
    servers: [{url: "http://127.0.0.1:1"}]
`
	y := startYarpWith(t, webConfig, routes, nil, rec.tel)

	get(t, y.url("web", "/other"), "")
	y.stop(t)

	server := rec.onlySpan(t, trace.SpanKindServer)
	checkAttrs(t, server, map[attribute.Key]string{routeKey: "", statusKey: "404"})
	if server.Status().Code == codes.Error {
		t.Error("server span is an error, want none for a 404")
	}
	if n := len(rec.spansOf(trace.SpanKindClient)); n != 0 {
		t.Errorf("got %d client spans, want 0", n)
	}
	checkCounts(t, serverDuration, rec.counts(t, serverDuration, routeKey), map[string]int64{"": 1})
	if got := rec.gauge(t, cacheEnabled); got != 0 {
		t.Errorf("%s = %d, want 0 with the cache off", cacheEnabled, got)
	}
}

// TestTelemetryContinuesTrace checks that a request with a traceparent is
// recorded in the client's trace.
func TestTelemetryContinuesTrace(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	y := startYarpWith(t, webConfig, appRoutes(newNamedBackend(t, "app")), nil, rec.tel)

	req := newRequest(t, http.MethodGet, y.url("web", "/"), "")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	do(t, req)
	y.stop(t)

	server := rec.onlySpan(t, trace.SpanKindServer)
	if got := server.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace ID = %s, want the client's", got)
	}
	if got := server.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span ID = %s, want the client's", got)
	}
}

// TestTelemetryBackendUnreachable checks that a backend nothing listens on
// marks both spans as errors.
func TestTelemetryBackendUnreachable(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t)
	y := startYarpWith(t, webConfig, appRoutes(closedURL(t)), nil, rec.tel)

	get(t, y.url("web", "/"), "")
	y.stop(t)

	server := rec.onlySpan(t, trace.SpanKindServer)
	client := rec.onlySpan(t, trace.SpanKindClient)
	checkAttrs(t, server, map[attribute.Key]string{statusKey: "502"})
	for _, span := range []sdktrace.ReadOnlySpan{server, client} {
		if span.Status().Code != codes.Error {
			t.Errorf("%s span status = %v, want %v", span.SpanKind(), span.Status().Code, codes.Error)
		}
	}
}
