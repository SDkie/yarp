package telemetry

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// fakeHandler is an inner slog handler that is enabled or not, and records
// the attributes and groups added to it.
type fakeHandler struct {
	enabled bool
	attrs   []slog.Attr
	groups  []string
}

func (h *fakeHandler) Enabled(context.Context, slog.Level) bool  { return h.enabled }
func (h *fakeHandler) Handle(context.Context, slog.Record) error { return nil }

func (h *fakeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fakeHandler{enabled: h.enabled, attrs: append(slices.Clone(h.attrs), attrs...), groups: h.groups}
}

func (h *fakeHandler) WithGroup(name string) slog.Handler {
	return &fakeHandler{enabled: h.enabled, attrs: h.attrs, groups: append(slices.Clone(h.groups), name)}
}

// TestLevelHandlerEnabled checks that a record is handled only at or above the level, and only if the inner handler is enabled.
func TestLevelHandlerEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		level        slog.Level
		innerEnabled bool
		want         bool
	}{
		{"below level", slog.LevelDebug, true, false},
		{"at level", slog.LevelInfo, true, true},
		{"above level", slog.LevelError, true, true},
		{"inner disabled", slog.LevelError, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := levelHandler{Handler: &fakeHandler{enabled: tt.innerEnabled}, level: slog.LevelInfo}
			if got := h.Enabled(context.Background(), tt.level); got != tt.want {
				t.Errorf("Enabled(%v) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

// TestLevelHandlerWith checks that WithAttrs and WithGroup reach the inner handler and keep following the level.
func TestLevelHandlerWith(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		with       func(slog.Handler) slog.Handler
		wantAttrs  int
		wantGroups int
	}{
		{"WithAttrs", func(h slog.Handler) slog.Handler { return h.WithAttrs([]slog.Attr{slog.String("k", "v")}) }, 1, 0},
		{"WithGroup", func(h slog.Handler) slog.Handler { return h.WithGroup("g") }, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			level := new(slog.LevelVar)
			level.Set(slog.LevelInfo)
			h := tt.with(levelHandler{Handler: &fakeHandler{enabled: true}, level: level})

			inner := h.(levelHandler).Handler.(*fakeHandler)
			if len(inner.attrs) != tt.wantAttrs || len(inner.groups) != tt.wantGroups {
				t.Errorf("inner handler has %d attrs and %d groups, want %d and %d",
					len(inner.attrs), len(inner.groups), tt.wantAttrs, tt.wantGroups)
			}
			if h.Enabled(context.Background(), slog.LevelDebug) {
				t.Error("Debug enabled at level Info")
			}
			level.Set(slog.LevelDebug)
			if !h.Enabled(context.Background(), slog.LevelDebug) {
				t.Error("Debug not enabled after the level changed to Debug")
			}
		})
	}
}

// memExporter keeps the bodies of the log records exported to it.
type memExporter struct {
	mu     sync.Mutex
	bodies []string
}

func (e *memExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.bodies = append(e.bodies, r.Body().AsString())
	}
	return nil
}

func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

// TestNewLogHandler checks that records at or above the level are exported to the logger provider.
func TestNewLogHandler(t *testing.T) {
	t.Parallel()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	logger := slog.New(newLogHandler(lp, slog.LevelWarn))

	logger.Info("dropped")
	logger.Warn("kept")
	logger.Error("also kept")

	exp.mu.Lock()
	defer exp.mu.Unlock()
	if want := []string{"kept", "also kept"}; !slices.Equal(exp.bodies, want) {
		t.Errorf("exported %q, want %q", exp.bodies, want)
	}
}
