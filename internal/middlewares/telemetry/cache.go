package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// SetCacheResult records the cache result, such as "hit", of the request
// with context ctx. It does nothing when telemetry is off.
func SetCacheResult(ctx context.Context, result string) {
	if info, ok := ctx.Value(infoCtxKey{}).(*requestInfo); ok {
		info.cacheResult = result
	}
}

// recordCacheResult counts the request by its cache result and adds the
// result to span. Requests that did not go through the cache are skipped.
func (t *Telemetry) recordCacheResult(ctx context.Context, span trace.Span, result string) {
	if result == "" {
		return
	}
	attr := cacheResultKey.String(result)
	t.metrics.cacheRequests.Add(ctx, 1, metric.WithAttributes(attr))
	span.SetAttributes(attr)
}

// MarkCacheEnabled records that the cache is on: yarp.cache.enabled is 0
// until then. It does nothing when telemetry is off.
func (t *Telemetry) MarkCacheEnabled() {
	if t != nil {
		t.metrics.cacheEnabled.Store(true)
	}
}
