package telemetry

import (
	"context"
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
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

const (
	serviceName    = "yarp"
	metricInterval = 15 * time.Second
)

// Setup starts exporting this version of yarp's telemetry to the collector
// in cfg and sets the otel globals. The default logger then also exports
// records at or above level, until Stop. A nil cfg leaves it off and returns
// a nil *Telemetry.
func Setup(cfg *config.Otel, version string, level slog.Leveler) (*Telemetry, error) {
	if cfg == nil {
		return nil, nil
	}
	ctx := context.Background()

	res, err := newResource(ctx, version)
	if err != nil {
		return nil, err
	}
	tp, err := newTracerProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}
	mp, err := newMeterProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}
	lp, err := newLoggerProvider(ctx, cfg.Endpoint, res)
	if err != nil {
		return nil, err
	}

	t, err := newTelemetry(tp, mp, lp)
	if err != nil {
		return nil, err
	}
	if err := otelruntime.Start(otelruntime.WithMeterProvider(mp)); err != nil {
		return nil, fmt.Errorf("start runtime metrics: %w", err)
	}

	// Register the providers and propagator globally too, for libraries that
	// use the otel globals.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetLoggerProvider(lp)
	otel.SetTextMapPropagator(t.propagator)

	t.console = slog.Default()

	// OpenTelemetry's own errors go to the console only, so a failed log
	// export is not exported again.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		t.console.Error("opentelemetry error", "error", err)
	}))

	slog.SetDefault(slog.New(slog.NewMultiHandler(t.console.Handler(), newLogHandler(lp, level))))
	slog.Info("opentelemetry configured", "endpoint", cfg.Endpoint)
	return t, nil
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
