// Unreachable-by-design branches (left uncovered intentionally):
//   - maintenance.go:237-239 (rotateLogs f.Close error): Close on a freshly
//     created O_WRONLY file cannot fail on a local filesystem (only EBADF,
//     which cannot occur here since the fd was just opened successfully).
//   - maintenance.go:282-284 and 323-325 (sweepPlans/sweepMedia WalkDir error
//     returns): dead defensive code — filepath.WalkDir only returns a non-nil
//     error when the walk callback returns one, and both callbacks always
//     return nil, so the error return can never execute.
package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSkipList seeds <home>/skills/.skipped.json with one entry per name.
func writeSkipList(t *testing.T, home string, entries map[string]time.Time) {
	t.Helper()
	skipped := make(map[string]any, len(entries))
	for name, at := range entries {
		skipped[name] = map[string]any{"skipped_at": at, "heuristic": "h", "times_skipped": 1}
	}
	data, err := json.Marshal(map[string]any{"skipped": skipped})
	if err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, filepath.Join(home, "skills", ".skipped.json"), data, time.Now())
}

// skipIfRoot skips permission-based tests when running as root, since root
// bypasses directory permission checks (mirrors the pattern used in
// cmd/odek/batch_patch_audit_test.go).
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
}

// ── Sweep orchestration ────────────────────────────────────────────────

// TestSweepStepFailureDoesNotBlockOthers forces the sessions step to fail
// (its directory is a regular file) and verifies the error is reported while
// the remaining steps still run.
func TestSweepStepFailureDoesNotBlockOthers(t *testing.T) {
	home := t.TempDir()
	// <home>/sessions is a regular file, so NewStoreWithDir cannot MkdirAll it.
	if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// A stale media file proves the later media step still ran.
	writeFileAt(t, filepath.Join(home, "media", "voice_chat1_x.ogg"), []byte("aaaa"), time.Now().Add(-2*time.Hour))

	cfg := DefaultConfig()
	cfg.AuditMaxAgeDays = 0
	cfg.LogMaxMB = 0
	cfg.PlansMaxAgeDays = 0
	cfg.SkillsSkipMaxAgeDays = 0

	rep, err := Sweep(context.Background(), home, cfg)
	if err == nil {
		t.Fatal("Sweep should report the sessions step failure")
	}
	if rep.MediaFreedBytes != 4 {
		t.Errorf("MediaFreedBytes = %d, want 4 (later steps must still run)", rep.MediaFreedBytes)
	}
}

// failAfterContext lets the first failAfter Err() calls pass and then reports
// context.Canceled, so tests can stop Sweep at a specific per-step gate.
type failAfterContext struct {
	context.Context
	failAfter int
	calls     int
}

func (c *failAfterContext) Err() error {
	c.calls++
	if c.calls > c.failAfter {
		return context.Canceled
	}
	return nil
}

// TestSweepContextCancelledAtEachGate cancels the context at every per-step
// gate past the first (gate 1 is covered by TestSweepContextCancelled).
func TestSweepContextCancelledAtEachGate(t *testing.T) {
	for failAfter := 1; failAfter <= 5; failAfter++ {
		ctx := &failAfterContext{Context: context.Background(), failAfter: failAfter}
		if _, err := Sweep(ctx, t.TempDir(), DefaultConfig()); err == nil {
			t.Errorf("failAfter=%d: Sweep should return the context error", failAfter)
		}
	}
}

// ── Start ──────────────────────────────────────────────────────────────

// TestStartDefaultIntervalFallback covers a non-positive interval falling
// back to the default 60-minute tick.
func TestStartDefaultIntervalFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := DefaultConfig()
	cfg.IntervalMinutes = 0 // must fall back to DefaultConfig().IntervalMinutes
	Start(ctx, t.TempDir(), cfg)
	cancel() // janitor goroutine must exit
}

// TestStartRunsSweepOnTick waits for the janitor's first tick (the smallest
// configurable interval is 1 minute) and verifies a failing sweep is reported
// on stderr. Slow by necessity — skipped in -short mode.
func TestStartRunsSweepOnTick(t *testing.T) {
	if testing.Short() {
		t.Skip("requires waiting for the 1-minute janitor tick")
	}
	home := t.TempDir()
	// Force the sweep to fail so the janitor reports it on stderr.
	if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.IntervalMinutes = 1 // smallest expressible tick
	Start(ctx, home, cfg)

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := r.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case msg := <-got:
		if !strings.Contains(msg, "maintenance sweep") {
			t.Errorf("stderr = %q, want a maintenance sweep error report", msg)
		}
	case <-time.After(75 * time.Second):
		t.Error("janitor did not run a sweep within 75s of Start")
	}
	cancel()
	os.Stderr = old
	w.Close()
	r.Close()
}

// ── sweepSessions / sweepAudit ─────────────────────────────────────────

// TestSweepAuditReadDirError covers a non-NotExist ReadDir failure: the audit
// path exists but is a regular file.
func TestSweepAuditReadDirError(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "audit"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := sweepAudit(home, 30); err == nil || !strings.Contains(err.Error(), "read audit dir") {
		t.Errorf("sweepAudit error = %v, want a read audit dir error", err)
	}
}

// TestSweepAuditSkipsNonJSONEntries covers the directory/non-.json skip branch.
func TestSweepAuditSkipsNonJSONEntries(t *testing.T) {
	home := t.TempDir()
	auditDir := filepath.Join(home, "sessions", "audit")
	old := time.Now().Add(-30 * 24 * time.Hour)
	writeFileAt(t, filepath.Join(auditDir, "old.json"), []byte(`{}`), old)
	writeFileAt(t, filepath.Join(auditDir, "notes.txt"), []byte("keep"), old) // non-json untouched
	if err := os.MkdirAll(filepath.Join(auditDir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}

	removed, err := sweepAudit(home, 14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only the old .json)", removed)
	}
	if _, err := os.Stat(filepath.Join(auditDir, "notes.txt")); err != nil {
		t.Error("non-json file should have been kept")
	}
	if _, err := os.Stat(filepath.Join(auditDir, "nested")); err != nil {
		t.Error("nested directory should have been kept")
	}
}

// TestSweepAuditInfoError makes an entry's Info() call fail by removing the
// search (execute) bit from the audit directory while keeping it readable.
func TestSweepAuditInfoError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	auditDir := filepath.Join(home, "sessions", "audit")
	writeFileAt(t, filepath.Join(auditDir, "old.json"), []byte(`{}`), time.Now().Add(-30*24*time.Hour))
	if err := os.Chmod(auditDir, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(auditDir, 0755) })

	removed, err := sweepAudit(home, 14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (unreadable entries are skipped)", removed)
	}
}

// TestSweepAuditRemoveError makes os.Remove fail by removing the write bit
// from the audit directory; the sweep must skip the file, not fail.
func TestSweepAuditRemoveError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	auditDir := filepath.Join(home, "sessions", "audit")
	writeFileAt(t, filepath.Join(auditDir, "old.json"), []byte(`{}`), time.Now().Add(-30*24*time.Hour))
	if err := os.Chmod(auditDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(auditDir, 0755) })

	removed, err := sweepAudit(home, 14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (unremovable file is skipped)", removed)
	}
}

// ── rotateLogs ─────────────────────────────────────────────────────────

// TestRotateLogsStatError covers a non-NotExist Stat failure via a symlink
// loop at the log path.
func TestRotateLogsStatError(t *testing.T) {
	home := t.TempDir()
	if err := os.Symlink("telegram.log", filepath.Join(home, "telegram.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := rotateLogs(home, 1); err == nil || !strings.Contains(err.Error(), "stat telegram.log") {
		t.Errorf("rotateLogs error = %v, want a stat error", err)
	}
}

// TestRotateLogsRenameError makes the rename fail by removing the write bit
// from the home directory.
func TestRotateLogsRenameError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	big := make([]byte, 2<<20)
	writeFileAt(t, filepath.Join(home, "telegram.log"), big, time.Now())
	if err := os.Chmod(home, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(home, 0755) })

	if _, err := rotateLogs(home, 1); err == nil || !strings.Contains(err.Error(), "rotate telegram.log") {
		t.Errorf("rotateLogs error = %v, want a rotate error", err)
	}
}

// TestRotateLogsRecreateError exhausts the process file-descriptor budget so
// the rename succeeds but recreating the fresh log fails.
func TestRotateLogsRecreateError(t *testing.T) {
	home := t.TempDir()
	big := make([]byte, 2<<20)
	writeFileAt(t, filepath.Join(home, "telegram.log"), big, time.Now())

	var fds []*os.File
	defer func() {
		for _, f := range fds {
			f.Close()
		}
	}()
	for {
		f, err := os.Open(os.DevNull)
		if err != nil {
			break // budget exhausted (EMFILE)
		}
		fds = append(fds, f)
	}

	_, err := rotateLogs(home, 1)
	if err == nil || !strings.Contains(err.Error(), "truncate telegram.log") {
		t.Errorf("rotateLogs error = %v, want a truncate error", err)
	}
	// The rename already happened, so the backup generation must exist.
	if _, statErr := os.Stat(filepath.Join(home, "telegram.log.1")); statErr != nil {
		t.Errorf("backup telegram.log.1 missing after rename: %v", statErr)
	}
}

// ── sweepPlans / sweepMedia ────────────────────────────────────────────

// TestSweepPlansStatError covers a non-NotExist Stat failure via a symlink
// loop at the plans root.
func TestSweepPlansStatError(t *testing.T) {
	home := t.TempDir()
	if err := os.Symlink("plans", filepath.Join(home, "plans")); err != nil {
		t.Fatal(err)
	}
	if _, err := sweepPlans(home, 30); err == nil || !strings.Contains(err.Error(), "stat plans dir") {
		t.Errorf("sweepPlans error = %v, want a stat plans dir error", err)
	}
}

// TestSweepPlansSkipsUnreadableDir makes the walk callback receive an error
// for an unreadable chat directory; the sweep must skip it and continue.
func TestSweepPlansSkipsUnreadableDir(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	writeFileAt(t, filepath.Join(home, "plans", "chat1", "old.md"), []byte("plan"), time.Now().Add(-60*24*time.Hour))
	if err := os.Chmod(filepath.Join(home, "plans", "chat1"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(home, "plans", "chat1"), 0755) })

	removed, err := sweepPlans(home, 30)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (unreadable dir skipped)", removed)
	}
}

// TestSweepPlansInfoError makes an entry's Info() call fail by removing the
// search bit from the plans root while keeping it readable.
func TestSweepPlansInfoError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	plansDir := filepath.Join(home, "plans")
	writeFileAt(t, filepath.Join(plansDir, "old.md"), []byte("plan"), time.Now().Add(-60*24*time.Hour))
	if err := os.Chmod(plansDir, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(plansDir, 0755) })

	removed, err := sweepPlans(home, 30)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (unreadable entries are skipped)", removed)
	}
}

// TestSweepMediaStatError covers a non-NotExist Stat failure via a symlink
// loop at the media root.
func TestSweepMediaStatError(t *testing.T) {
	home := t.TempDir()
	if err := os.Symlink("media", filepath.Join(home, "media")); err != nil {
		t.Fatal(err)
	}
	if _, err := sweepMedia(home); err == nil || !strings.Contains(err.Error(), "stat media dir") {
		t.Errorf("sweepMedia error = %v, want a stat media dir error", err)
	}
}

// TestSweepMediaInfoError makes an entry's Info() call fail by removing the
// search bit from the media root while keeping it readable.
func TestSweepMediaInfoError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	mediaDir := filepath.Join(home, "media")
	writeFileAt(t, filepath.Join(mediaDir, "voice_chat1_x.ogg"), []byte("aaaa"), time.Now().Add(-2*time.Hour))
	if err := os.Chmod(mediaDir, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(mediaDir, 0755) })

	freed, err := sweepMedia(home)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 0 {
		t.Errorf("freed = %d, want 0 (unreadable entries are skipped)", freed)
	}
}

// ── gcSkipList ─────────────────────────────────────────────────────────

// TestGCSkipListKeepsFreshEntries covers the removed == 0 early return with a
// non-empty skip list.
func TestGCSkipListKeepsFreshEntries(t *testing.T) {
	home := t.TempDir()
	writeSkipList(t, home, map[string]time.Time{"fresh-skill": time.Now().UTC()})

	removed, err := gcSkipList(home, 90)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (no expired entries)", removed)
	}
}

// TestGCSkipListSaveError makes the skip-list rewrite fail by removing the
// write bit from the skills directory.
func TestGCSkipListSaveError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	writeSkipList(t, home, map[string]time.Time{"old-skill": time.Now().Add(-120 * 24 * time.Hour).UTC()})
	skillsDir := filepath.Join(home, "skills")
	// WriteFile truncates the existing .skipped.json in place, so the file
	// itself must be read-only (dir write permission alone is not checked
	// when opening an existing file).
	if err := os.Chmod(filepath.Join(skillsDir, ".skipped.json"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(skillsDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(skillsDir, 0755) })

	removed, err := gcSkipList(home, 90)
	if err == nil || !strings.Contains(err.Error(), "save skip list") {
		t.Errorf("gcSkipList error = %v, want a save skip list error", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (counted before the save failed)", removed)
	}
}
