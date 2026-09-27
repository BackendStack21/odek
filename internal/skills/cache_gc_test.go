package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Save must keep only user-dir entries plus the current project's skill
// dir; entries for other projects are dropped.
func TestSavePersistentCache_DropsOtherProjectEntries(t *testing.T) {
	userDir := t.TempDir()
	projectA := filepath.Join(t.TempDir(), "project-a")
	projectB := filepath.Join(t.TempDir(), "project-b")

	fc := fileCache{
		filepath.Join(userDir, "alpha", "SKILL.md"):  time.Unix(1, 0),
		filepath.Join(projectA, "beta", "SKILL.md"):  time.Unix(2, 0),
		filepath.Join(projectB, "gamma", "SKILL.md"): time.Unix(3, 0),
	}
	prev := skillCache{
		filepath.Join(userDir, "alpha", "SKILL.md"):  {Skill: Skill{Name: "alpha"}},
		filepath.Join(projectA, "beta", "SKILL.md"):  {Skill: Skill{Name: "beta"}},
		filepath.Join(projectB, "gamma", "SKILL.md"): {Skill: Skill{Name: "gamma"}},
	}

	savePersistentCache(userDir, projectB, fc, prev)

	data, err := os.ReadFile(cachePath(userDir))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	s := string(data)
	if strings.Contains(s, projectA) {
		t.Errorf("persisted cache still contains project-A path:\n%s", s)
	}
	if !strings.Contains(s, filepath.Join(userDir, "alpha")) {
		t.Errorf("persisted cache lost user-dir entry:\n%s", s)
	}
	if !strings.Contains(s, filepath.Join(projectB, "gamma")) {
		t.Errorf("persisted cache lost current project entry:\n%s", s)
	}
}
