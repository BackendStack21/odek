package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A home reached through a symlink (HOME=/home/user with /home linked into
// another volume, as on macOS) is the same home as its physical directory:
// the protected paths under it classify the same either way.
func TestSymlinkedHomeKeepsProtectedPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(root, "data", "user")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "home")
	if err := os.Symlink(filepath.Join(root, "data"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(link, "user"))

	dirs := currentHomeDirs()
	if len(dirs) != 2 || dirs[1] != strings.TrimPrefix(physical, "/private") {
		t.Fatalf("currentHomeDirs() = %q, want the spelled and the physical home", dirs)
	}
	for _, cmd := range []string{
		"echo x > " + filepath.Join(link, "user", ".bashrc"),
		"git archive --output=" + filepath.Join(link, "user", ".bashrc") + " HEAD",
		"tar -cf " + filepath.Join(link, "user", ".ssh", "x.tar") + " y",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	if got := ClassifyPath(filepath.Join(link, "user", "notes.txt")); got != LocalWrite {
		t.Errorf("an ordinary file in the symlinked home = %s, want local_write", got)
	}
	if isSystemPath(filepath.Join(physical, "notes.txt")) {
		t.Errorf("the physical home is not a system path")
	}
}
