package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/maintenance"
)

// The cleanup dry-run preview must mirror the artifacts sweep exactly —
// including the task-granular unfiled bucket (the sweep's whole reason for
// existing). A preview that lists only root-level dirs would silently
// under-report the unfiled backlog.
func TestCleanupPreview_ListsAgedUnfiledTaskDirs(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	oldTask := filepath.Join(artDir, "unfiled", "task-x")
	freshTask := filepath.Join(artDir, "unfiled", "task-y")
	for _, d := range []string{oldTask, freshTask} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "report.md"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldTask, aged, aged); err != nil {
		t.Fatal(err)
	}

	c := collectCleanupCandidates(home, maintenance.Config{ArtifactsMaxAgeHours: 24})
	if len(c.artifacts) != 1 {
		t.Fatalf("preview must list exactly the aged unfiled task, got %v", c.artifacts)
	}
	if rel, err := filepath.Rel(artDir, c.artifacts[0]); err != nil || filepath.ToSlash(rel) != "unfiled/task-x" {
		t.Errorf("preview listed %v, want unfiled/task-x", c.artifacts)
	}
}
