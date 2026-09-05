package maintenance

// Bug-hunt v3 (fix/bughunt-v3) RED-first regression tests.
//
// 1. DaysAgo overflow: retention day counts come from operator config with
//    no upper clamp. time.Duration(days)*24h is int64 nanoseconds and wraps
//    for days > 106751, sending the cutoff INTO THE FUTURE — the first
//    janitor tick then deletes every unpinned session (index UpdatedAt is
//    always before a future cutoff).
// 2. sweepArtifacts wholesale TOCTOU: a session dir planned for wholesale
//    RemoveAll can gain a fresh task between plan and execute (the dir mtime
//    freshens); the wholesale path must re-verify staleness at execute time
//    or it destroys the fresh task the prune path would have protected.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDaysAgo_LargeRetentionDoesNotWrapIntoFuture pins the overflow guard:
// any positive day count must produce a cutoff in the past, and absurdly
// large values must clamp (100 years ≈ forever) rather than wrap.
func TestDaysAgo_LargeRetentionDoesNotWrapIntoFuture(t *testing.T) {
	now := time.Now()
	for _, days := range []int{1, 30, 365, 106751, 120000, 999999} {
		cutoff := DaysAgo(now, days)
		if !cutoff.Before(now) {
			t.Fatalf("DaysAgo(now, %d) = %v did not land before now — duration overflow wrapped the retention cutoff into the future (mass-deletion vector)", days, cutoff)
		}
	}
	// The clamp must be monotonic-idempotent: anything beyond the clamp
	// behaves like the clamp bound, never like a wrapped negative.
	if !DaysAgo(now, 999999).Equal(DaysAgo(now, 36500)) {
		t.Fatalf("DaysAgo(now, 999999) = %v != clamped DaysAgo(now, 36500) = %v", DaysAgo(now, 999999), DaysAgo(now, 36500))
	}
	// Same guard for the unexported now-based twin used by the sweeps.
	if !daysAgo(999999).Before(time.Now()) {
		t.Fatalf("daysAgo(999999) did not land before now — overflow wrap")
	}
}

// sweepArtifacts removes delegate_tasks artifacts past retention (see
// re-check on the wholesale RemoveAll path: a session dir that was aged at
// plan time but gained a fresh task before execute must NOT be wholesale-
// removed — the fresh task survives and the dir is handled by the per-child
// path on the next sweep.
func TestSweepArtifacts_WholesaleSkipsFreshenedParents(t *testing.T) {
	home := t.TempDir()
	aged := filepath.Join(home, "artifacts", "sess-aged")
	expiredTask := filepath.Join(aged, "task-expired")
	if err := os.MkdirAll(expiredTask, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(expiredTask, "result.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(aged, old, old); err != nil {
		t.Fatal(err)
	}

	freshTask := filepath.Join(aged, "task-fresh")
	prev := sweepArtifactsHook
	sweepArtifactsHook = func() {
		// Race window: a delegate task lands in the parent the instant the
		// sweep holds a stale wholesale plan. This freshens the dir mtime.
		if err := os.MkdirAll(freshTask, 0o755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(freshTask, "live.txt"), []byte("live"), 0o644); err != nil {
			t.Error(err)
		}
	}
	defer func() { sweepArtifactsHook = prev }()

	if _, _, err := sweepArtifacts(home, 24*time.Hour); err != nil {
		t.Fatalf("sweepArtifacts: %v", err)
	}

	if _, err := os.Stat(filepath.Join(freshTask, "live.txt")); err != nil {
		t.Fatalf("fresh task created during the plan→execute window was destroyed by wholesale RemoveAll: %v", err)
	}
}
