package badgerdb

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestBadgerLogger checks that Badger's logs reach slog at the right levels.
func TestBadgerLogger(t *testing.T) {
	// Not parallel: it replaces the default logger.
	var buf bytes.Buffer
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var l badgerLogger
	l.Errorf("e %d\n", 1)
	l.Warningf("w %d", 2)
	l.Infof("i %d", 3)
	l.Debugf("d %d", 4) // below the level: dropped

	for _, want := range []string{
		`level=ERROR msg="e 1" component=badger`,
		`level=WARN msg="w 2" component=badger`,
		`level=INFO msg="i 3" component=badger`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "d 4") {
		t.Errorf("debug message logged at INFO level:\n%s", buf.String())
	}
}
