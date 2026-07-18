package telegram

import (
	"fmt"
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

// makeTestHome creates a temporary directory that is treated as $HOME for the
// duration of the test. It is placed under the real user home directory so it
// is not classified as a system temp directory by danger.ClassifyPath, which
// would otherwise mask secret-subtree checks.
func makeTestHome(t *testing.T) string {
	t.Helper()
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	home, err := os.MkdirTemp(realHome, "odek_test_home_*")
	if err != nil {
		t.Fatalf("mkdir test home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
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

// TestResolveMediaPath_TildeExpansion verifies that a leading ~ is expanded to
// the home directory and resolved inside the odek media dir.
func TestResolveMediaPath_TildeExpansion(t *testing.T) {
	setupMediaPathTest(t)

	dir, err := MediaDir()
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "tilde-media.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveMediaPath("~/.odek/media/tilde-media.txt")
	if err != nil {
		t.Fatalf("ResolveMediaPath with tilde: %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Errorf("expected absolute resolved path, got %q", resolved)
	}
}

// TestResolveMediaPath_RejectsSymlinkToAllowedFile verifies that the final
// component being a symlink is rejected even when the symlink target is inside
// the allowlist. This exercises the O_NOFOLLOW / lstat check.
func TestResolveMediaPath_RejectsSymlinkToAllowedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on windows")
	}

	setupMediaPathTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Both target and link are inside the allowed cwd.
	target := filepath.Join(cwd, "allowed-target.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "allowed-link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	_, err = ResolveMediaPath(link)
	if err == nil {
		t.Fatal("expected rejection for symlink final component")
	}
	if !strings.Contains(err.Error(), "symlinks are not allowed") {
		t.Errorf("expected 'symlinks are not allowed' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsSensitiveSubtrees verifies that secret subtrees
// under $HOME are rejected even when CWD == $HOME makes them technically
// inside the allowlist.
func TestResolveMediaPath_RejectsSensitiveSubtrees(t *testing.T) {
	home := makeTestHome(t)
	t.Chdir(home)

	cases := []string{".ssh/id_rsa", ".aws/credentials", ".gnupg/secring.gpg"}
	for _, sub := range cases {
		t.Run(sub, func(t *testing.T) {
			f := filepath.Join(home, sub)
			if err := os.MkdirAll(filepath.Dir(f), 0700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(f, []byte("secret"), 0600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := ResolveMediaPath(f)
			if err == nil {
				t.Fatalf("expected rejection for sensitive path %s", f)
			}
			if !strings.Contains(err.Error(), "rejected sensitive path") {
				t.Errorf("expected 'rejected sensitive path' in error, got: %v", err)
			}
		})
	}
}

// TestResolveMediaPath_RejectsEnvFiles verifies that .env* files are rejected
// even when they live inside an otherwise allowed project directory.
func TestResolveMediaPath_RejectsEnvFiles(t *testing.T) {
	home := makeTestHome(t)
	t.Chdir(home)

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f := filepath.Join(project, ".env.local")
	if err := os.WriteFile(f, []byte("SECRET=1"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ResolveMediaPath(f)
	if err == nil {
		t.Fatal("expected rejection for .env file")
	}
	if !strings.Contains(err.Error(), "rejected .env file") {
		t.Errorf("expected 'rejected .env file' in error, got: %v", err)
	}
}

// TestResolveMediaPath_RejectsOdekTrustAnchors verifies that ~/.odek trust
// anchors are rejected while ~/.odek/media remains allowed for re-upload.
func TestResolveMediaPath_RejectsOdekTrustAnchors(t *testing.T) {
	home := makeTestHome(t)
	t.Chdir(home)

	// Trust anchor must be rejected.
	cfg := filepath.Join(home, ".odek", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ResolveMediaPath(cfg); err == nil {
		t.Fatal("expected rejection for ~/.odek/config.json")
	} else if !strings.Contains(err.Error(), "rejected sensitive path") {
		t.Errorf("expected 'rejected sensitive path' in error, got: %v", err)
	}

	// Media dir must remain allowed.
	mediaDir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}
	mf := filepath.Join(mediaDir, "allowed-media.txt")
	if err := os.WriteFile(mf, []byte("x"), 0644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if _, err := ResolveMediaPath(mf); err != nil {
		t.Fatalf("media dir file should be allowed: %v", err)
	}
}

// TestResolveMediaPathForChat_AllowsOwnChat verifies that files tagged for the
// requesting chat inside ~/.odek/media are accepted.
func TestResolveMediaPathForChat_AllowsOwnChat(t *testing.T) {
	setupMediaPathTest(t)

	mediaDir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}

	chatID := int64(12345)
	cases := []string{
		fmt.Sprintf("doc_chat%d_report.pdf", chatID),
		fmt.Sprintf("photo_chat%d_abc.jpg", chatID),
		fmt.Sprintf("voice_chat%d_def.ogg", chatID),
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(mediaDir, name)
			if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
				t.Fatalf("write media file: %v", err)
			}
			if _, err := ResolveMediaPathForChat(f, chatID); err != nil {
				t.Fatalf(" ResolveMediaPathForChat(%q, %d) error: %v", f, chatID, err)
			}
		})
	}
}

// TestResolveMediaPathForChat_RejectsOtherChat verifies that files tagged for a
// different chat inside ~/.odek/media are rejected, preventing cross-chat
// re-disclosure of downloaded documents or media.
func TestResolveMediaPathForChat_RejectsOtherChat(t *testing.T) {
	setupMediaPathTest(t)

	mediaDir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}

	ownerChat := int64(12345)
	attackerChat := int64(99999)

	cases := []string{
		fmt.Sprintf("doc_chat%d_report.pdf", ownerChat),
		fmt.Sprintf("photo_chat%d_abc.jpg", ownerChat),
		fmt.Sprintf("voice_chat%d_def.ogg", ownerChat),
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(mediaDir, name)
			if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
				t.Fatalf("write media file: %v", err)
			}
			_, err := ResolveMediaPathForChat(f, attackerChat)
			if err == nil {
				t.Fatalf("expected rejection for other chat's file: %s", f)
			}
			if !strings.Contains(err.Error(), "different chat") {
				t.Errorf("expected 'different chat' in error, got: %v", err)
			}
		})
	}
}

// TestResolveMediaPathForChat_AllowsChatSubdir verifies that files under a
// per-chat subdirectory inside ~/.odek/media are accepted.
func TestResolveMediaPathForChat_AllowsChatSubdir(t *testing.T) {
	setupMediaPathTest(t)

	mediaDir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}

	chatID := int64(12345)
	subdir := filepath.Join(mediaDir, fmt.Sprintf("chat%d", chatID))
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir chat subdir: %v", err)
	}

	f := filepath.Join(subdir, "shared-notes.pdf")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if _, err := ResolveMediaPathForChat(f, chatID); err != nil {
		t.Fatalf("ResolveMediaPathForChat(%q, %d) error: %v", f, chatID, err)
	}
}

// TestResolveMediaPathForChat_BackwardCompatibility verifies that the
// unscoped ResolveMediaPath still accepts any file in ~/.odek/media, preserving
// behavior for callers that do not know the originating chat.
func TestResolveMediaPathForChat_BackwardCompatibility(t *testing.T) {
	setupMediaPathTest(t)

	mediaDir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}

	f := filepath.Join(mediaDir, "doc_chat12345_report.pdf")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if _, err := ResolveMediaPath(f); err != nil {
		t.Fatalf("ResolveMediaPath(%q) should still allow any media file: %v", f, err)
	}
}

// TestBroadBaseWarning verifies that a warning is produced when the bot is
// launched from $HOME or /.
func TestBroadBaseWarning(t *testing.T) {
	home := makeTestHome(t)

	t.Run("home cwd warns", func(t *testing.T) {
		t.Chdir(home)
		if w := BroadBaseWarning(); w == "" {
			t.Error("expected warning when cwd == $HOME")
		}
	})

	t.Run("root cwd warns", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("root cwd test is Unix-specific")
		}
		t.Chdir("/")
		if w := BroadBaseWarning(); w == "" {
			t.Error("expected warning when cwd == /")
		}
	})

	t.Run("normal cwd is silent", func(t *testing.T) {
		sub := t.TempDir()
		t.Chdir(sub)
		if w := BroadBaseWarning(); w != "" {
			t.Errorf("expected no warning, got %q", w)
		}
	})
}
