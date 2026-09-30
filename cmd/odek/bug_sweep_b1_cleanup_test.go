package main

// Regression: cleanup dry-run must preview artifact subtree removals.
//
// RED-first: both failed against the pre-fix dry-run collector, which never
// previewed artifact subtree deletions (the real sweep removes
// ~/.odek/artifacts/<session_id>/ by default).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/maintenance"
)

func TestCleanupDryRun_PreviewsArtifactSubtrees(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts", "sess-b2")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "result.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(artDir, old, old); err != nil {
		t.Fatal(err)
	}

	cfg := maintenance.Config{ArtifactsMaxAgeHours: 24}
	c := collectCleanupCandidates(home, cfg)
	for _, a := range c.artifacts {
		if a == artDir {
			return
		}
	}
	t.Fatalf("dry-run omitted artifact subtree %q that the real sweep deletes; previewed artifacts = %v", artDir, c.artifacts)
}
