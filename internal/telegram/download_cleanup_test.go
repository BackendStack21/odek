package telegram

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupMedia_RemovesOldFiles(t *testing.T) {
	tmp := t.TempDir()
	mediaDir := filepath.Join(tmp, ".odek", "media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", tmp)

	// Create an old file (2 hours ago).
	oldPath := filepath.Join(mediaDir, "voice_oldfile.oga")
	if err := os.WriteFile(oldPath, []byte("old-data"), 0600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}

	// Create a recent file (10 minutes ago).
	recentPath := filepath.Join(mediaDir, "photo_newfile.jpg")
	if err := os.WriteFile(recentPath, []byte("new-data"), 0600); err != nil {
		t.Fatalf("write new: %v", err)
	}
	recentTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(recentPath, recentTime, recentTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	// Clean up files older than 1 hour.
	removed, err := CleanupMedia(1 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupMedia: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed file, got %d", removed)
	}

	// Old file should be gone.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old file should have been removed")
	}

	// Recent file should still exist.
	if _, err := os.Stat(recentPath); err != nil {
		t.Error("recent file should still exist")
	}
}

func TestCleanupMedia_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	mediaDir := filepath.Join(tmp, ".odek", "media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", tmp)

	removed, err := CleanupMedia(1 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupMedia on empty dir: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed from empty dir, got %d", removed)
	}
}

func TestCleanupMedia_DirNotExist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Media dir doesn't exist yet — should not error.
	removed, err := CleanupMedia(1 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupMedia on non-existent dir: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}
}

func TestCleanupMedia_AllFilesRecent(t *testing.T) {
	tmp := t.TempDir()
	mediaDir := filepath.Join(tmp, ".odek", "media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", tmp)

	// Create 3 recent files.
	for i := 0; i < 3; i++ {
		name := filepath.Join(mediaDir, "recent_"+string(rune('a'+i))+".ogg")
		if err := os.WriteFile(name, []byte("data"), 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	removed, err := CleanupMedia(1 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupMedia: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed (all recent), got %d", removed)
	}
}

func TestCleanupMedia_NonMediaFilesIgnored(t *testing.T) {
	tmp := t.TempDir()
	mediaDir := filepath.Join(tmp, ".odek", "media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("HOME", tmp)

	// Create a subdirectory — should be skipped.
	subDir := filepath.Join(mediaDir, "subdir")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	// Create an old file.
	oldPath := filepath.Join(mediaDir, "old.oga")
	if err := os.WriteFile(oldPath, []byte("data"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	removed, err := CleanupMedia(1 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupMedia: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// Subdirectory should still exist.
	if _, err := os.Stat(subDir); err != nil {
		t.Error("subdirectory should not have been removed")
	}
}
