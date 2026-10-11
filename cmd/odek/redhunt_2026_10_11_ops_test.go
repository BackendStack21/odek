package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/runtimelog"
	"github.com/BackendStack21/odek/internal/session"
)

// `odek cleanup --dry-run` lists pinned sessions as deletion candidates,
// while the real sweep (Store.Cleanup) skips them. The preview claims to
// match the sweep exactly, so an operator sees bookmarked sessions in the
// "would remove" count that the real run keeps.
func TestRED_CleanupDryRunListsPinnedSessions(t *testing.T) {
	home := t.TempDir()
	store, err := session.NewStoreWithDir(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{{Role: "user", Content: "keep me"}}, "m", "pinned")
	if err != nil {
		t.Fatal(err)
	}
	sess.Pinned = true
	sess.UpdatedAt = time.Now().Add(-60 * 24 * time.Hour)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	// An unpinned expired session is still listed and removed.
	old, err := store.Create([]session.Message{{Role: "user", Content: "drop me"}}, "m", "old")
	if err != nil {
		t.Fatal(err)
	}
	old.UpdatedAt = time.Now().Add(-60 * 24 * time.Hour)
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}

	cfg := maintenance.Config{SessionsMaxAgeDays: 30}
	preview := collectCleanupCandidates(home, cfg)

	rep, err := maintenance.Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SessionsRemoved != 1 {
		t.Fatalf("precondition: sweep removed %d sessions, want 1 (the unpinned one)", rep.SessionsRemoved)
	}
	if len(preview.sessions) != rep.SessionsRemoved {
		t.Fatalf("dry run lists %d session(s) %v, real sweep removes %d (pinned sessions are kept)", len(preview.sessions), preview.sessions, rep.SessionsRemoved)
	}
	if preview.sessions[0] != store.Path(old.ID) {
		t.Fatalf("dry run lists %v, want the unpinned session %s", preview.sessions, store.Path(old.ID))
	}
}

// The dry-run runtime-log preview clamps max_age_hours with
// ClampRetentionHours (36500) while the sweep's PruneAtStartup clamps at
// 87600, so for max_age_hours in (36500, 87600] the preview's cutoff is
// years later than the sweep's and it reports records as expired that the
// real cleanup keeps.
func TestRED_CleanupDryRunRuntimeLogClampDiffersFromSweep(t *testing.T) {
	home := t.TempDir()
	logPath := filepath.Join(home, "runtime.log")
	old := time.Now().Add(-50000 * time.Hour).UTC().Format(time.RFC3339Nano)
	line := `{"schema":"odek.log/v1","timestamp":"` + old + `","level":"info","event":"run.started","process_id":"p","record_id":"p-1","pid":1}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := maintenance.Config{RuntimeLog: runtimelog.Options{Path: logPath, MaxFiles: 2, MaxAgeHours: 80000}}

	out := captureStdout(func() { printCleanupDryRun(home, cfg) })

	rep, err := maintenance.Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RuntimeLogRecordsRemoved != 0 {
		t.Fatalf("precondition: sweep removed %d records", rep.RuntimeLogRecordsRemoved)
	}
	if strings.Contains(out, "runtime records expired: 1") {
		t.Fatalf("dry run reports a record expired that the sweep keeps (max_age_hours=80000):\n%s", out)
	}

	// Above the ceiling both sides clamp to the same bound: a record older
	// than the ceiling is previewed and then removed.
	ancient := time.Now().Add(-90000 * time.Hour).UTC().Format(time.RFC3339Nano)
	line = `{"schema":"odek.log/v1","timestamp":"` + ancient + `","level":"info","event":"run.started","process_id":"p","record_id":"p-2","pid":1}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeLog.MaxAgeHours = 1 << 40
	out = captureStdout(func() { printCleanupDryRun(home, cfg) })
	if !strings.Contains(out, "runtime records expired: 1") {
		t.Fatalf("dry run misses a record past the retention ceiling:\n%s", out)
	}
	rep, err = maintenance.Sweep(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RuntimeLogRecordsRemoved != 1 {
		t.Fatalf("sweep removed %d records past the ceiling, want 1", rep.RuntimeLogRecordsRemoved)
	}
}
