package main

// Bug-sweep batch 1 (fix/bug-hunt-b1) — B2/B3 regression tests.
//
// RED-first: both failed against the pre-fix dry-run collector, which never
// previewed artifact subtree deletions (the real sweep removes
// ~/.odek/artifacts/<session_id>/ by default) and whose log list omitted
// serve.log even though rotateLogs rotates it.

import (
	"bytes"
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

func TestCleanupDryRun_IncludesServeLog(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "serve.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("a"), 2*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := maintenance.Config{LogMaxMB: 1}
	c := collectCleanupCandidates(home, cfg)
	for _, l := range c.logs {
		if l == p {
			return
		}
	}
	t.Fatalf("dry-run omitted oversized serve.log %q; previewed logs = %v", p, c.logs)
}
