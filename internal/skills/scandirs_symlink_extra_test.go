package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDirsSkipsDirsWithoutSkillFileAndKeepsRegular(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, dir, "ok", "name: ok\ndescription: fine\n", "regular body")
	res := ScanDirs("", dir, nil)
	n := 0
	for _, s := range append(append([]Skill{}, res.AutoLoad...), res.Lazy...) {
		n++
		if s.Name != "ok" {
			t.Fatalf("unexpected skill %q", s.Name)
		}
	}
	if n != 1 {
		t.Fatalf("want 1 skill, got %d", n)
	}
}
