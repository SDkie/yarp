package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	scopeName   = "github.com/SDkie/yarp"
	stopTimeout = 5 * time.Second
)

// Telemetry records yarp's spans, metrics and logs. A nil *Telemetry means
// it is off: callers then skip Handler and Transport, and its other methods
// do nothing.
type Telemetry struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	metrics    metrics

	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider

	console *slog.Logger // the default logger before Setup, restored by Stop
}

// newTelemetry returns a Telemetry that records to tp, mp and lp; Stop
// shuts them down.
func newTelemetry(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider, lp *sdklog.LoggerProvider) (*Telemetry, error) {
	m, err := newMetrics(mp.Meter(scopeName))
	if err != nil {
		return nil, err
	}
	// Trace context travels in the traceparent and tracestate headers,
	// baggage in the baggage header.
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	return &Telemetry{
		tracer:         tp.Tracer(scopeName),
		propagator:     propagator,
		metrics:        m,
		tracerProvider: tp,
		meterProvider:  mp,
		loggerProvider: lp,
	}, nil
}

// Stop restores the console logger, then flushes and shuts down the
// providers.
func (t *Telemetry) Stop() {
	if t == nil {
		return
	}
	if t.console != nil {
		slog.SetDefault(t.console)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	err := errors.Join(
		t.tracerProvider.Shutdown(ctx),
		t.meterProvider.Shutdown(ctx),
		t.loggerProvider.Shutdown(ctx),
	)
	if err != nil {
		slog.Error("failed to stop OpenTelemetry", "error", err)
	}
}
