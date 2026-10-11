package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/runtimelog"
)

// The audit store quarantines an unreadable log as
// "<id>.json.corrupt-<timestamp>" (session.AuditStore). Those copies hold
// the same user messages and ingest records as the live log, but
// sweepAudit only removes names whose extension is exactly ".json", so a
// quarantined copy outlives audit_max_age_days forever. The dry-run
// candidate list must agree with the sweep.
func TestRED_SweepAuditKeepsQuarantinedCorruptCopies(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, "20250101-abc.json")
	corrupt := filepath.Join(dir, "20250101-abc.json.corrupt-20250101T000000.000000000")
	unrelated := filepath.Join(dir, "notes.txt")
	fresh := filepath.Join(dir, "20261011-new.json.corrupt-20261011T000000.000000000")
	for _, p := range []string{live, corrupt, unrelated, fresh} {
		if err := os.WriteFile(p, []byte(`{"type":"turn","turn":{"user_message":"my password is hunter2"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if p == fresh {
			continue
		}
		old := time.Now().Add(-100 * 24 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	preview := AuditSweepCandidates(home, 14)
	if len(preview) != 2 {
		t.Fatalf("dry-run candidates = %v, want the live log and its quarantined copy", preview)
	}
	rep, err := Sweep(context.Background(), home, Config{AuditMaxAgeDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	if rep.AuditRemoved != len(preview) {
		t.Fatalf("sweep removed %d audit files, preview listed %d", rep.AuditRemoved, len(preview))
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Fatalf("precondition: expired live audit log not removed: %v", err)
	}
	if _, err := os.Stat(corrupt); err == nil {
		t.Fatalf("expired quarantined audit copy %s survived the audit retention sweep", filepath.Base(corrupt))
	}
	for _, keep := range []string{unrelated, fresh} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s should survive the sweep: %v", filepath.Base(keep), err)
		}
	}
}

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
