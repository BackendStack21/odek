package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// ScanDirs (uncached, used after any dirty reload) must refuse a symlinked
// SKILL.md just like scanDirCached and the docs say.
func TestRED_ScanDirsRefusesSymlinkedSkillFile(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "real.md")
	if err := os.WriteFile(target, []byte("---\nname: linked\ndescription: x\nodek:\n  auto_load: true\n---\n\nbody text here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	if err := os.MkdirAll(filepath.Join(user, "linked"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(user, "linked", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	res := ScanDirs("", user, nil)
	for _, s := range append(append([]Skill{}, res.AutoLoad...), res.Lazy...) {
		if s.Name == "linked" {
			t.Fatalf("symlinked SKILL.md was loaded by ScanDirs")
		}
	}
}
