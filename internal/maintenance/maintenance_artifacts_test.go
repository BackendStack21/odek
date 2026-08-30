package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// M1 backstop: the janitor sweeps artifact subtrees past retention
// (crash leftovers, hand-deleted sessions) and reports the removals.
func TestSweepArtifacts_RemovesAgedKeepsFresh(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	old := filepath.Join(artDir, "sess-old")
	fresh := filepath.Join(artDir, "sess-fresh")
	for _, d := range []string{filepath.Join(old, "task-1"), fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "report.md"), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := sweepArtifacts(home, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("want 1 artifact subtree removed, got %d", removed)
	}
	if freed != int64(len("payload")) {
		t.Errorf("freed bytes: got %d", freed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("aged subtree must be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh subtree must be kept")
	}
}

func TestSweepArtifacts_ZeroKeepsForever(t *testing.T) {
	home := t.TempDir()
	d := filepath.Join(home, "artifacts", "sess-x")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, _, err := sweepArtifacts(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("retention 0 = keep forever, got %d removals", removed)
	}
}
