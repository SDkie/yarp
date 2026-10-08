package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
)

// metrics holds the instruments yarp records to.
type metrics struct {
	serverDuration  httpconv.ServerRequestDuration
	serverActive    httpconv.ServerActiveRequests
	clientDuration  httpconv.ClientRequestDuration
	cacheRequests   metric.Int64Counter
	requestOverhead metric.Float64Histogram
	// cacheEnabled is read by the yarp.cache.enabled gauge on the exporter's
	// goroutine. It is a pointer because an atomic.Bool must not be copied.
	cacheEnabled *atomic.Bool
}

// overheadBuckets are the bucket boundaries of yarp.request.overhead, in
// seconds: 1µs to 250ms, as yarp's own time is far below a backend's.
var overheadBuckets = []float64{
	0.000001, 0.0000025, 0.000005,
	0.00001, 0.000025, 0.00005,
	0.0001, 0.00025, 0.0005,
	0.001, 0.0025, 0.005,
	0.01, 0.025, 0.05,
	0.1, 0.25,
}

func newMetrics(meter metric.Meter) (metrics, error) {
	var m metrics
	var errs [6]error
	m.serverDuration, errs[0] = httpconv.NewServerRequestDuration(meter)
	m.serverActive, errs[1] = httpconv.NewServerActiveRequests(meter)
	m.clientDuration, errs[2] = httpconv.NewClientRequestDuration(meter)
	m.cacheRequests, errs[3] = meter.Int64Counter("yarp.cache.requests",
		metric.WithDescription("Requests by cache result: hit, miss or bypass."),
		metric.WithUnit("{request}"))
	m.requestOverhead, errs[4] = meter.Float64Histogram("yarp.request.overhead",
		metric.WithDescription("Time yarp adds to a request: its time to the response headers minus the backend's."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(overheadBuckets...))
	m.cacheEnabled = new(atomic.Bool)
	_, errs[5] = meter.Int64ObservableGauge("yarp.cache.enabled",
		metric.WithDescription("1 if the HTTP cache is enabled, 0 if not."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			var v int64
			if m.cacheEnabled.Load() {
				v = 1
			}
			o.Observe(v)
			return nil
		}))
	if err := errors.Join(errs[:]...); err != nil {
		return metrics{}, fmt.Errorf("create metric instruments: %w", err)
	}
	return m, nil
}
