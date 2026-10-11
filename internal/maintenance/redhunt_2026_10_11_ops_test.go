package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/runtimelog"
)

// RuntimeLogExpiredPreview resolves the same policy as Sweep (including the
// unexpanded default path) and counts what the sweep's prune removes.
func TestRED_RuntimeLogExpiredPreviewMatchesSweep(t *testing.T) {
	home := t.TempDir()
	if n, err := RuntimeLogExpiredPreview(context.Background(), home, Config{}); n != 0 || err != nil {
		t.Fatalf("no runtime log configured: %d, %v", n, err)
	}
	ts := time.Now().Add(-90000 * time.Hour).UTC().Format(time.RFC3339Nano)
	line := `{"schema":"odek.log/v1","timestamp":"` + ts + `","level":"info","event":"run.started","process_id":"p","record_id":"p-1","pid":1}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "runtime.log"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{RuntimeLog: runtimelog.Options{Path: "~/.odek/runtime.log", MaxFiles: 2, MaxAgeHours: 1 << 40}}
	n, err := RuntimeLogExpiredPreview(context.Background(), home, cfg)
	if err != nil || n != 1 {
		t.Fatalf("preview = %d, %v; want 1", n, err)
	}
	rep, err := Sweep(context.Background(), home, cfg)
	if err != nil || rep.RuntimeLogRecordsRemoved != n {
		t.Fatalf("sweep removed %d (%v), preview said %d", rep.RuntimeLogRecordsRemoved, err, n)
	}
}
