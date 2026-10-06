package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// RecordCacheResult counts a request by its cache result, such as "hit",
// and adds the result to the request's span. It does nothing when telemetry
// is off.
func (t *Telemetry) RecordCacheResult(ctx context.Context, result string) {
	if t == nil {
		return
	}
	attr := cacheResultKey.String(result)
	t.metrics.cacheRequests.Add(ctx, 1, metric.WithAttributes(attr))
	trace.SpanFromContext(ctx).SetAttributes(attr)
}
