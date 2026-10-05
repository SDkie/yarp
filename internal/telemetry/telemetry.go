// Package telemetry sends traces, metrics and logs to an OpenTelemetry
// collector over OTLP/HTTP.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SDkie/yarp/internal/config"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

const (
	serviceName    = "yarp"
	scopeName      = "github.com/SDkie/yarp"
	stopTimeout    = 5 * time.Second
	metricInterval = 15 * time.Second
)

// Telemetry is a running OpenTelemetry setup. A nil *Telemetry means it is
// off: its methods then do nothing and add no overhead.
type Telemetry struct {
	console        *slog.Logger // the default logger before Setup
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider
}

// Setup starts OpenTelemetry for this version of yarp and also sends the
// default logger's records at or above level to it. A nil cfg leaves it off
// and returns a nil *Telemetry.
func Setup(cfg *config.Otel, version string, level slog.Leveler) (*Telemetry, error) {
	if cfg == nil {
		return nil, nil
	}
	ctx := context.Background()

	res, err := newResource(ctx, version)
	if err != nil {
		return nil, err
	}
	tracerProvider, err := newTracerProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}
	meterProvider, err := newMeterProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}
	loggerProvider, err := newLoggerProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	if err := otelruntime.Start(otelruntime.WithMeterProvider(meterProvider)); err != nil {
		return nil, fmt.Errorf("start runtime metrics: %w", err)
	}
	otel.SetLoggerProvider(loggerProvider)

	// Trace IDs travel in the traceparent header.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// OpenTelemetry's own errors go to the console only, so a failed log
	// export is not exported again.
	console := slog.Default()
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		console.Error("opentelemetry error", "error", err)
	}))

	slog.SetDefault(slog.New(slog.NewMultiHandler(
		console.Handler(),
		newLogHandler(loggerProvider, level),
	)))
	slog.Info("opentelemetry configured", "endpoint", cfg.Endpoint)

	return &Telemetry{
		console:        console,
		tracerProvider: tracerProvider,
		meterProvider:  meterProvider,
		loggerProvider: loggerProvider,
	}, nil
}

// Stop flushes and shuts down OpenTelemetry.
func (t *Telemetry) Stop() {
	if t == nil {
		return
	}
	slog.SetDefault(t.console)
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	err := errors.Join(
		t.tracerProvider.Shutdown(ctx),
		t.meterProvider.Shutdown(ctx),
		t.loggerProvider.Shutdown(ctx),
	)
	if err != nil {
		t.console.Error("failed to stop OpenTelemetry", "error", err)
	}
}

// newResource describes this process; OTEL_* environment variables override it.
func newResource(ctx context.Context, version string) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(serviceName), semconv.ServiceVersion(version)),
		resource.WithFromEnv(), // last, so it wins
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}
	return res, nil
}

func newTracerProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(getSignalURL(endpoint, "traces")),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
	), nil
}

func newMeterProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(getSignalURL(endpoint, "metrics")),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, getMetricReaderOptions()...)),
	), nil
}

func newLoggerProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(getSignalURL(endpoint, "logs")),
		otlploghttp.WithCompression(otlploghttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	), nil
}

// getMetricReaderOptions exports metrics every metricInterval unless
// OTEL_METRIC_EXPORT_INTERVAL is set.
func getMetricReaderOptions() []sdkmetric.PeriodicReaderOption {
	if os.Getenv("OTEL_METRIC_EXPORT_INTERVAL") != "" {
		return nil
	}
	return []sdkmetric.PeriodicReaderOption{sdkmetric.WithInterval(metricInterval)}
}

// getSignalURL returns endpoint plus "/v1/<signal>".
func getSignalURL(endpoint, signal string) string {
	u, _ := url.Parse(endpoint) // validated by the config
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/" + signal
	return u.String()
}
