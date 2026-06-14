package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCleanAbs(t *testing.T) {
	got, err := CleanAbs("/foo/bar/../baz")
	if err != nil {
		t.Fatalf("CleanAbs error: %v", err)
	}
	if want := filepath.Clean("/foo/baz"); got != want {
		t.Errorf("CleanAbs = %q, want %q", got, want)
	}
}

func TestResolveDirSymlinks_LeavesFinalComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// link/file: directory symlink resolved, final component untouched.
	resolved := ResolveDirSymlinks(filepath.Join(link, "file"))
	want := filepath.Join(target, "file")
	// On macOS the temp dir itself may be reached through a symlink (/var ->
	// /private/var), so resolve the target directory before appending the final
	// component.
	if r, err := filepath.EvalSymlinks(target); err == nil {
		want = filepath.Join(r, "file")
	}
	if resolved != want {
		t.Errorf("ResolveDirSymlinks = %q, want %q", resolved, want)
	}
}

func TestResolveDirSymlinks_FallsBackWhenParentMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "file")
	got := ResolveDirSymlinks(missing)
	want, err := CleanAbs(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolveDirSymlinks missing-parent = %q, want %q", got, want)
	}
}

func TestWithinRoot_Lexical(t *testing.T) {
	if !WithinRoot("/workspace", "/workspace/extra") {
		t.Error("expected /workspace/extra to be under /workspace")
	}
	if WithinRoot("/workspace", "/tmp") {
		t.Error("expected /tmp not to be under /workspace")
	}
	// Separator-aware: /workspacefoo must not match /workspace prefix.
	if WithinRoot("/workspace", "/workspacefoo") {
		t.Error("expected /workspacefoo not to be under /workspace")
	}
}

func TestWithinRoot_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workdir, "link")); err != nil {
		t.Fatal(err)
	}

	// workdir/link/secret resolves outside workdir.
	if WithinRoot(workdir, filepath.Join(workdir, "link", "secret")) {
		t.Error("expected symlinked directory escape to be rejected")
	}
}

func TestWithinRoot_MissingRootFallsBack(t *testing.T) {
	// The root does not exist. The comparison should fall back to lexical
	// paths so legitimate under-root candidates are still accepted.
	if !WithinRoot("/home/alice/project", "/home/alice/project/data") {
		t.Error("expected under-root candidate to be accepted when root does not exist")
	}
	if WithinRoot("/home/alice/project", "/tmp") {
		t.Error("expected outside candidate to be rejected when root does not exist")
	}
}

func TestWithinRoot_Equality(t *testing.T) {
	if !WithinRoot("/workspace", "/workspace") {
		t.Error("expected root to be considered inside itself")
	}
}
