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

// The shared "unfiled" bucket (tasks whose parent session id was unknown)
// is swept at TASK granularity: its own mtime refreshes on every delegation,
// so parent-mtime aging never fires under daily use. Old tasks must go,
// fresh ones stay, and the bucket survives while it still holds tasks.
func TestSweepArtifacts_UnfiledSweptPerTask(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	oldTask := filepath.Join(artDir, "unfiled", "task-x")
	freshTask := filepath.Join(artDir, "unfiled", "task-y")
	for _, d := range []string{oldTask, freshTask} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "report.md"), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Age only the task dir; the unfiled parent stays fresh (task-y creation
	// refreshed it) — exactly the on-disk shape daily use produces.
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldTask, aged, aged); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := sweepArtifacts(home, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("want 1 task subtree removed, got %d", removed)
	}
	if freed != int64(len("payload")) {
		t.Errorf("freed bytes: got %d", freed)
	}
	if _, err := os.Stat(oldTask); !os.IsNotExist(err) {
		t.Error("aged unfiled task must be removed")
	}
	if _, err := os.Stat(freshTask); err != nil {
		t.Error("fresh unfiled task must be kept")
	}
	if _, err := os.Stat(filepath.Join(artDir, "unfiled")); err != nil {
		t.Error("unfiled parent holding a fresh task must be kept")
	}
}

// A fresh task inside an unfiled parent whose mtime is old (mtime anomalies,
// restored backups) must survive: unfiled is never wholesale-removed by its
// own mtime — only its task children age out individually.
func TestSweepArtifacts_OldUnfiledParentFreshTaskSurvives(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	unfiled := filepath.Join(artDir, "unfiled")
	freshTask := filepath.Join(unfiled, "task-y")
	if err := os.MkdirAll(freshTask, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(freshTask, "report.md"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(unfiled, aged, aged); err != nil {
		t.Fatal(err)
	}

	removed, _, err := sweepArtifacts(home, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("nothing should be removed, got %d", removed)
	}
	if _, err := os.Stat(freshTask); err != nil {
		t.Error("fresh task inside old-mtime unfiled parent must survive")
	}
	if _, err := os.Stat(unfiled); err != nil {
		t.Error("unfiled parent with live tasks must survive")
	}
}

// A parent left empty by the task sweep is pruned: an empty session dir or
// unfiled bucket carries no value. Removal counts the task subtree and the
// pruned parent.
func TestSweepArtifacts_EmptyParentPruned(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	unfiled := filepath.Join(artDir, "unfiled")
	oldTask := filepath.Join(unfiled, "task-x")
	if err := os.MkdirAll(oldTask, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldTask, "report.md"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldTask, aged, aged); err != nil {
		t.Fatal(err)
	}

	removed, _, err := sweepArtifacts(home, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("want 2 removals (task subtree + pruned parent), got %d", removed)
	}
	if _, err := os.Stat(oldTask); !os.IsNotExist(err) {
		t.Error("aged task must be removed")
	}
	if _, err := os.Stat(unfiled); !os.IsNotExist(err) {
		t.Error("emptied parent must be pruned")
	}
}

// Session dirs keep the wholesale backstop (their own mtime), but a still-
// live session dir holding one ancient task gets task-granular cleanup too.
func TestSweepArtifacts_FreshSessionDirOldTaskSwept(t *testing.T) {
	home := t.TempDir()
	artDir := filepath.Join(home, "artifacts")
	sess := filepath.Join(artDir, "sess-live")
	oldTask := filepath.Join(sess, "task-old")
	freshTask := filepath.Join(sess, "task-new")
	for _, d := range []string{oldTask, freshTask} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldTask, aged, aged); err != nil {
		t.Fatal(err)
	}

	removed, _, err := sweepArtifacts(home, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("want 1 task subtree removed, got %d", removed)
	}
	if _, err := os.Stat(oldTask); !os.IsNotExist(err) {
		t.Error("aged task inside live session must be removed")
	}
	if _, err := os.Stat(freshTask); err != nil {
		t.Error("fresh task inside live session must be kept")
	}
	if _, err := os.Stat(sess); err != nil {
		t.Error("live session dir must be kept")
	}
}

// The dry-run preview (odek cleanup --dry-run) must list EXACTLY what the
// sweep removes: expired task subtrees in any parent, wholesale aged
// session dirs, and parents the sweep empties. Both consume the same plan,
// so this fixture pins them together.
func TestArtifactsSweepCandidates_PreviewMatchesSweep(t *testing.T) {
	seed := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		artDir := filepath.Join(home, "artifacts")
		mk := func(rel string, age time.Duration) {
			t.Helper()
			d := filepath.Join(artDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "report.md"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if age > 0 {
				when := time.Now().Add(-age)
				if err := os.Chtimes(d, when, when); err != nil {
					t.Fatal(err)
				}
			}
		}
		mk("unfiled/task-old", 48*time.Hour)     // expired task in shared bucket
		mk("unfiled/task-fresh", 0)              // survives; keeps unfiled alive
		mk("sess-aged/task-1", 0)                // parent aged below → wholesale
		mk("sess-live/task-old", 48*time.Hour)   // expired task in live session
		mk("sess-live/task-new", 0)              // survives
		aged := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(filepath.Join(artDir, "sess-aged"), aged, aged); err != nil {
			t.Fatal(err)
		}
		return home
	}
	previewHome := seed(t)
	sweepHome := seed(t)

	got := ArtifactsSweepCandidates(previewHome, 24*time.Hour)
	want := map[string]bool{
		"unfiled/task-old":   true,
		"sess-aged":          true,
		"sess-live/task-old": true,
	}
	if len(got) != len(want) {
		t.Fatalf("preview listed %d paths, want %d: %v", len(got), len(want), got)
	}
	for _, p := range got {
		rel, err := filepath.Rel(filepath.Join(previewHome, "artifacts"), p)
		if err != nil {
			t.Fatal(err)
		}
		if !want[filepath.ToSlash(rel)] {
			t.Errorf("preview listed unexpected path %q", rel)
		}
	}

	// The twin home must lose exactly those paths to the real sweep.
	if _, _, err := sweepArtifacts(sweepHome, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	artDir := filepath.Join(sweepHome, "artifacts")
	for rel := range want {
		if _, err := os.Stat(filepath.Join(artDir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("sweep did not remove %q", rel)
		}
	}
	for _, keep := range []string{"unfiled", "unfiled/task-fresh", "sess-live", "sess-live/task-new"} {
		if _, err := os.Stat(filepath.Join(artDir, filepath.FromSlash(keep))); err != nil {
			t.Errorf("sweep must keep %q: %v", keep, err)
		}
	}
}
