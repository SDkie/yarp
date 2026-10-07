package badgerdb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// badgerLogger sends BadgerDB's logs to the default slog logger.
type badgerLogger struct{}

func (badgerLogger) Errorf(format string, args ...any)   { logBadger(slog.LevelError, format, args) }
func (badgerLogger) Warningf(format string, args ...any) { logBadger(slog.LevelWarn, format, args) }
func (badgerLogger) Infof(format string, args ...any)    { logBadger(slog.LevelInfo, format, args) }
func (badgerLogger) Debugf(format string, args ...any)   { logBadger(slog.LevelDebug, format, args) }

// logBadger formats the message only when level is enabled.
func logBadger(level slog.Level, format string, args []any) {
	ctx := context.Background()
	if !slog.Default().Enabled(ctx, level) {
		return
	}
	slog.Log(ctx, level, strings.TrimSpace(fmt.Sprintf(format, args...)), "component", "badger")
}
