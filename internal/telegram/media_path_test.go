package telegram

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupMediaPathTest saves the real home directory, overrides HOME for the
// test so MediaDir resolves under a temp directory, and returns an outside
// directory (under the real home but not in any allowlist) for negative tests.
func setupMediaPathTest(t *testing.T) (outsideDir string) {
	t.Helper()

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	outsideDir = filepath.Join(realHome, "odek_media_path_test_outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(outsideDir)
	})

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	// Run with a temp working directory so tests that exercise the "cwd is
	// allowed" path write their fixtures into a throwaway directory instead of
	// polluting the package source tree. t.Chdir restores the original cwd on
	// cleanup.
	t.Chdir(t.TempDir())

	return outsideDir
}

// TestResolveMediaPath_AllowedDirs verifies that files inside the allowed
// directories (cwd, ~/.odek/media, temp dir) are accepted.
func TestResolveMediaPath_AllowedDirs(t *testing.T) {
	setupMediaPathTest(t)

	cases := []struct {
		name string
		make func() string
	}{
		{
			name: "cwd",
			make: func() string {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				f := filepath.Join(cwd, "allowed-cwd.txt")
				if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
				return f
			},
		},
		{
			name: "odek media dir",
			make: func() string {
				dir, err := MediaDir()
				if err != nil {
					t.Fatal(err)
				}
				f := filepath.Join(dir, "allowed-media.txt")
				if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
				return f
			},
		},
		{
			name: "temp dir",
			make: func() string {
				f := filepath.Join(os.TempDir(), "allowed-temp.txt")
				if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(f) })
				return f
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.make()
			resolved, err := ResolveMediaPath(path)
			if err != nil {
				t.Fatalf("ResolveMediaPath(%q) error: %v", path, err)
			}
			if resolved == "" {
				t.Fatal("expected non-empty resolved path")
			}
		})
	}
}

// TestResolveMediaPath_RejectsOutsideAllowlist verifies that paths outside the
// allowed directories are rejected.
func TestResolveMediaPath_RejectsOutsideAllowlist(t *testing.T) {
	outsideDir := setupMediaPathTest(t)

	f := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveMediaPath(f)
	if err == nil {
		t.Fatalf("expected rejection for path outside allowlist: %s", f)
	}
	if !strings.Contains(err.Error(), "outside allowed") {
		t.Errorf("expected 'outside allowed' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsSymlink verifies that symlinks are rejected.
func TestResolveMediaPath_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}

	outsideDir := setupMediaPathTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Symlink in cwd pointing to a file outside the allowlist.
	target := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "link-to-secret.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	_, err = ResolveMediaPath(link)
	if err == nil {
		t.Fatal("expected rejection for symlink")
	}
	if !strings.Contains(err.Error(), "symlinks are not allowed") {
		t.Errorf("expected 'symlinks are not allowed' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsSymlinkTraversal verifies that a path which
// traverses a symlink to escape the allowlist is rejected.
func TestResolveMediaPath_RejectsSymlinkTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}

	outsideDir := setupMediaPathTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside cwd that points to a directory outside the
	// allowlist.
	dirLink := filepath.Join(cwd, "linkdir")
	if err := os.Symlink(outsideDir, dirLink); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dirLink) })

	// Create a real file under the outside directory.
	target := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dirLink, "secret.txt")
	_, err = ResolveMediaPath(path)
	if err == nil {
		t.Fatal("expected rejection for symlink traversal outside allowlist")
	}
	if !strings.Contains(err.Error(), "outside allowed") {
		t.Errorf("expected 'outside allowed' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsDirectory verifies that directories are rejected.
func TestResolveMediaPath_RejectsDirectory(t *testing.T) {
	setupMediaPathTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cwd, "subdir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	_, err = ResolveMediaPath(dir)
	if err == nil {
		t.Fatal("expected rejection for directory")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected 'not a regular file' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsNonexistent verifies that missing files are
// rejected.
func TestResolveMediaPath_RejectsNonexistent(t *testing.T) {
	setupMediaPathTest(t)

	_, err := ResolveMediaPath(filepath.Join(os.TempDir(), "does-not-exist.txt"))
	if err == nil {
		t.Fatal("expected rejection for nonexistent file")
	}
}

// TestResolveMediaPath_Empty verifies that an empty path is rejected.
func TestResolveMediaPath_Empty(t *testing.T) {
	_, err := ResolveMediaPath("")
	if err == nil {
		t.Fatal("expected rejection for empty path")
	}
}

// TestResolveMediaPath_RelativeInCWD verifies that relative paths under cwd are
// resolved and accepted.
func TestResolveMediaPath_RelativeInCWD(t *testing.T) {
	setupMediaPathTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f := "relative-allowed.txt"
	if err := os.WriteFile(filepath.Join(cwd, f), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveMediaPath(f)
	if err != nil {
		t.Fatalf("ResolveMediaPath(%q) error: %v", f, err)
	}
	if !filepath.IsAbs(resolved) {
		t.Errorf("expected absolute resolved path, got %q", resolved)
	}
}
