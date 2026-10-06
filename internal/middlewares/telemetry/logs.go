package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// newLogHandler returns a slog handler that exports records at or above
// level to lp.
func newLogHandler(lp *sdklog.LoggerProvider, level slog.Leveler) slog.Handler {
	return levelHandler{
		Handler: otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp)),
		level:   level,
	}
}

// levelHandler drops records below level; the otelslog handler has no level
// option of its own.
type levelHandler struct {
	slog.Handler
	level slog.Leveler
}

func (h levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.Handler.Enabled(ctx, l)
}

func (h levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelHandler{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{Handler: h.Handler.WithGroup(name), level: h.level}
}
