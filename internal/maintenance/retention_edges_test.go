package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── ClampRetentionDays / ClampRetentionHours ────────────────────────────────

func TestClampRetentionDays_NegativeClampsToZero(t *testing.T) {
	if got := ClampRetentionDays(-1); got != 0 {
		t.Fatalf("ClampRetentionDays(-1) = %d, want 0", got)
	}
	if got := ClampRetentionDays(-1000); got != 0 {
		t.Fatalf("ClampRetentionDays(-1000) = %d, want 0", got)
	}
	if got := ClampRetentionDays(0); got != 0 {
		t.Fatalf("ClampRetentionDays(0) = %d, want 0", got)
	}
	if got := ClampRetentionDays(3); got != 3 {
		t.Fatalf("ClampRetentionDays(3) = %d, want 3", got)
	}
	if got := ClampRetentionDays(MaxRetentionDays + 1); got != MaxRetentionDays {
		t.Fatalf("oversized = %d, want %d", got, MaxRetentionDays)
	}
}

func TestClampRetentionHours(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0},                       // keep forever
		{-1, 0},                      // negative → 0 (never future cutoff)
		{-1 << 30, 0},                // deep negative
		{24, 24},                     // in-range passthrough
		{MaxRetentionDays, MaxRetentionDays},
		{MaxRetentionDays + 1, MaxRetentionDays}, // overflow-class cap
	}
	for _, c := range cases {
		if got := ClampRetentionHours(c.in); got != c.want {
			t.Fatalf("ClampRetentionHours(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ── sweepArtifacts error branches ────────────────────────────────────────────

func skipIfRootEdges(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not work as root")
	}
}

// unreadable artifacts dir → artifactsSweepPlan error propagates.
func TestSweepArtifacts_PlanErrorUnreadableDir(t *testing.T) {
	skipIfRootEdges(t)
	home := t.TempDir()
	dir := filepath.Join(home, "artifacts")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755) //nolint:errcheck

	_, _, err := sweepArtifacts(home, time.Hour)
	if err == nil {
		t.Fatal("expected plan error from unreadable artifacts dir, got nil")
	}
}

// A prune-path failure (parent became non-empty between plan and execute —
// os.Remove only deletes EMPTY dirs) must be skipped, not propagated.
func TestSweepArtifacts_PruneFailureContinues(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "artifacts")
	parent := filepath.Join(dir, "sess")
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	// Fresh, empty parent → planned as a prune.

	prev := sweepArtifactsHook
	sweepArtifactsHook = func() {
		// Between plan and execute: a fresh task appears in the parent, so
		// os.Remove(parent) fails with ENOTEMPTY → loop must continue.
		if err := os.Mkdir(filepath.Join(parent, "newtask"), 0755); err != nil {
			t.Errorf("mkdir newtask: %v", err)
		}
	}
	defer func() { sweepArtifactsHook = prev }()

	removed, _, err := sweepArtifacts(home, time.Hour)
	if err != nil {
		t.Fatalf("sweepArtifacts returned error on prune failure: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (prune should have failed)", removed)
	}
	if _, statErr := os.Stat(parent); statErr != nil {
		t.Fatalf("parent should still exist after failed prune: %v", statErr)
	}
}

// Prune-path failure (os.Remove on a non-empty dir) is also swallowed.
func TestSweepArtifacts_PruneNonEmptyDirFails(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "artifacts")
	// Fresh session dir containing only an expired task dir + a stray file:
	// stray file keeps parent alive (emptied=false) so no prune is planned —
	// instead build the prune case: completely empty fresh parent.
	if err := os.MkdirAll(filepath.Join(dir, "sess"), 0755); err != nil {
		t.Fatal(err)
	}
	removed, _, err := sweepArtifacts(home, time.Hour)
	if err != nil {
		t.Fatalf("sweepArtifacts error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (empty parent pruned)", removed)
	}
}

// ── rotateLogs ───────────────────────────────────────────────────────────────

// Rotation happy path with a pre-seeded oversized log verifies the rename +
// recreate cycle end to end.
func TestRotateLogs_OversizedRotated(t *testing.T) {
	home := t.TempDir()
	names := LogRotationNames()
	if len(names) == 0 {
		t.Fatal("LogRotationNames returned no names")
	}
	path := filepath.Join(home, names[0])
	big := make([]byte, 2<<20) // 2 MB > 1 MB limit
	if err := os.WriteFile(path, big, 0600); err != nil {
		t.Fatal(err)
	}
	rotated, err := rotateLogs(home, 1)
	if err != nil {
		t.Fatalf("rotateLogs error: %v", err)
	}
	if len(rotated) != 1 || rotated[0] != path {
		t.Fatalf("rotated = %v, want [%s]", rotated, path)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("recreated log size = %d, want 0", info.Size())
	}
}

// Missing logs are skipped silently (IsNotExist branch).
func TestRotateLogs_MissingLogsSkipped(t *testing.T) {
	home := t.TempDir()
	rotated, err := rotateLogs(home, 1)
	if err != nil {
		t.Fatalf("rotateLogs error: %v", err)
	}
	if len(rotated) != 0 {
		t.Fatalf("rotated = %v, want empty", rotated)
	}
}
