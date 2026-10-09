package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeDocker puts a stub "docker" on PATH that records its argv and succeeds,
// so InjectFiles can run without a Docker daemon.
func fakeDocker(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub not supported on windows")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" >> " + filepath.Join(dir, "calls.log") + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A file reached through an in-cwd directory symlink that points outside cwd
// must not be copied into the container.
func TestRED_InjectFilesThroughSymlinkedDirectory(t *testing.T) {
	fakeDocker(t)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("host secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}
	n, err := InjectFiles("odek-test", []string{"link/secret.txt"}, cwd)
	if err != nil {
		t.Fatalf("InjectFiles: %v", err)
	}
	if n != 0 {
		t.Fatalf("file reached through symlinked dir outside cwd was injected (n=%d)", n)
	}
}

// A symlinked directory that stays inside cwd is fine, and a plain file is
// still injected.
func TestInjectFiles_SymlinkedDirInsideCwdAndPlainFile(t *testing.T) {
	fakeDocker(t)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, "real"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"real/a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(cwd, f), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(cwd, "real"), filepath.Join(cwd, "alias")); err != nil {
		t.Fatal(err)
	}
	n, err := InjectFiles("odek-test", []string{"alias/a.txt", "b.txt"}, cwd)
	if err != nil {
		t.Fatalf("InjectFiles: %v", err)
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2", n)
	}
}
