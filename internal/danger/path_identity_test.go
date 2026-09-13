package danger

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellWriteResolvesProtectedAliases(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".envrc")
	alias := filepath.Join(dir, "notes")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		if exists {
			if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, cmd := range []string{"printf marker > " + alias, "tee " + alias, "cp source " + alias, "dd of=" + alias} {
			if got := Classify(cmd); got != Persistence {
				t.Errorf("%q (exists %v): got %s", cmd, exists, got)
			}
		}
		if got := ClassifyPathWrite(alias); got != Persistence {
			t.Errorf("native alias: got %s", got)
		}
	}
}

func TestShellWriteResolvesNewTargetBelowAlias(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "build")
	if err := os.Symlink(hooks, alias); err != nil {
		t.Fatal(err)
	}
	if got := Classify("printf marker > " + filepath.Join(alias, "new", "hook")); got != Persistence {
		t.Fatalf("got %s", got)
	}
}

func TestShellAliasSwapReclassifies(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "notes")
	if err := os.Symlink(filepath.Join(dir, "ordinary"), alias); err != nil {
		t.Fatal(err)
	}
	command := "printf marker > " + alias
	if got := Classify(command); got != LocalWrite {
		t.Fatalf("before swap: %s", got)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, ".envrc"), alias); err != nil {
		t.Fatal(err)
	}
	if got := Classify(command); got != Persistence {
		t.Fatalf("after swap: %s", got)
	}
}

func TestPathResolutionDoesNotCleanParentsBeforeLinks(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(filepath.Join(hooks, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "build")
	if err := os.Symlink(filepath.Join(hooks, "nested"), alias); err != nil {
		t.Fatal(err)
	}
	if got := Classify("printf marker > " + alias + "/../new-hook"); got != Persistence {
		t.Fatalf("got %s", got)
	}
}
