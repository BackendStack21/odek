package main

// TDD RED phase — staging-dir hygiene (fix/staging-sweep). The workspace
// staging root (.odek-artifacts/) had three gaps: crash-orphans with
// content were never cleaned (the janitor only knows ~/.odek), the empty
// parent persisted, and staged deliverables were visible to the user's
// git. Every artifact-bearing child run now self-heals its workspace.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureStagingRoot_Gitignore(t *testing.T) {
	cwd := t.TempDir()
	ensureStagingRoot(cwd)

	gitignore := filepath.Join(cwd, stagingDirName, ".gitignore")
	b, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("staging root must carry a self-gitignore: %v", err)
	}
	if string(b) != "*\n!.gitignore\n" {
		t.Errorf("gitignore content = %q, want %q", b, "*\n!.gitignore\n")
	}
	// Idempotent: a second call must not fail or duplicate.
	ensureStagingRoot(cwd)
	b2, _ := os.ReadFile(gitignore)
	if string(b2) != "*\n!.gitignore\n" {
		t.Errorf("second ensure must keep content stable: %q", b2)
	}
}

func TestSweepStagingOrphans_AgedRemovedOthersKept(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, stagingDirName)
	aged := filepath.Join(root, "task-dead")
	fresh := filepath.Join(root, "task-live")
	current := "task-current"
	for _, d := range []string{filepath.Join(aged, "t"), fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(aged, "t", "big.bin"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(aged, old, old); err != nil {
		t.Fatal(err)
	}

	removed := sweepStagingOrphans(cwd, current, 24*time.Hour)
	if removed != 1 {
		t.Fatalf("want 1 orphan removed, got %d", removed)
	}
	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Error("aged orphan must be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh sibling must be kept (may be an in-flight parallel task)")
	}
}

func TestSweepStagingOrphans_MissingRootNoop(t *testing.T) {
	if n := sweepStagingOrphans(t.TempDir(), "task-x", 24*time.Hour); n != 0 {
		t.Errorf("missing staging root must be a no-op, got %d", n)
	}
}

func TestStagingRoot_TidiedAfterRelocation(t *testing.T) {
	// After relocation the staging root keeps only the self-gitignore —
	// no empty task dirs, no deliverable residue.
	root := t.TempDir()
	staging := filepath.Join(root, stagingDirName, "task-1")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	writeArtifactFile(t, staging, "report.md", "content")
	canonical := filepath.Join(root, "canonical")

	if _, err := relocateStagingArtifacts(staging, canonical); err != nil {
		t.Fatal(err)
	}
	ensureStagingRoot(root)

	entries, err := os.ReadDir(filepath.Join(root, stagingDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("task dir %q must be gone after relocation", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(canonical, "report.md")); err != nil {
		t.Errorf("relocated artifact missing: %v", err)
	}
}
