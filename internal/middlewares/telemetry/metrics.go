package telemetry

import (
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
)

// metrics holds the instruments yarp records to.
type metrics struct {
	serverDuration httpconv.ServerRequestDuration
	serverActive   httpconv.ServerActiveRequests
	clientDuration httpconv.ClientRequestDuration
	cacheRequests  metric.Int64Counter
}

func newMetrics(meter metric.Meter) (metrics, error) {
	var m metrics
	var errs [4]error
	m.serverDuration, errs[0] = httpconv.NewServerRequestDuration(meter)
	m.serverActive, errs[1] = httpconv.NewServerActiveRequests(meter)
	m.clientDuration, errs[2] = httpconv.NewClientRequestDuration(meter)
	m.cacheRequests, errs[3] = meter.Int64Counter("yarp.cache.requests",
		metric.WithDescription("Requests by cache result: hit, miss or bypass."),
		metric.WithUnit("{request}"))
	if err := errors.Join(errs[:]...); err != nil {
		return metrics{}, fmt.Errorf("create metric instruments: %w", err)
	}
	return m, nil
}
