package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// The lazily created memory directory is owner-only, and a path that cannot
// be a directory surfaces a lock error rather than panicking.
func TestLockFactsDirCreatesOwnerOnlyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "memory")
	unlock, err := lockFactsDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", fi.Mode().Perm())
	}
}

func TestLockFactsDirFailsWhenParentIsFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFactsDir(filepath.Join(base, "memory")); err == nil {
		t.Fatal("expected error")
	}
}
