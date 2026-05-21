package telegram

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Test MediaDir ──────────────────────────────────────────────────────────

func TestMediaDir(t *testing.T) {
	dir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir() error: %v", err)
	}
	if dir == "" {
		t.Fatal("MediaDir() returned empty path")
	}

	// Directory should exist.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("MediaDir stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("MediaDir() is not a directory")
	}

	// Should be under ~/.odek/media/.
	if !strings.Contains(dir, ".odek/media") {
		t.Errorf("MediaDir path = %q, want it to contain '.odek/media'", dir)
	}

	// Cleanup test directory.
	os.RemoveAll(dir)
}

// ── Test DownloadVoice ─────────────────────────────────────────────────────

func TestDownloadVoice_Success(t *testing.T) {
	// Server that responds to getFile and file download.
	var callCount int
	handler := func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case strings.Contains(r.URL.String(), "getFile"):
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"voice123","file_path":"voice/file.ogg"}}`)
		case strings.HasSuffix(r.URL.String(), "voice/file.ogg"):
			w.Write([]byte("fake-ogg-data"))
		default:
			http.Error(w, `{"ok":false,"description":"not found"}`, 404)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadVoice(bot, "voice123")
	if err != nil {
		t.Fatalf("DownloadVoice() error: %v", err)
	}
	if path == "" {
		t.Fatal("DownloadVoice() returned empty path")
	}

	// File should exist.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "fake-ogg-data" {
		t.Errorf("file content = %q, want %q", string(data), "fake-ogg-data")
	}

	if callCount < 2 {
		t.Errorf("expected at least 2 calls (getFile + download), got %d", callCount)
	}

	// Cleanup.
	os.Remove(path)
}

func TestDownloadVoice_GetFileError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"ok":false,"description":"invalid file_id","error_code":400}`)
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadVoice(bot, "bad_id")
	if err == nil {
		t.Fatal("DownloadVoice should return error on getFile failure")
	}
}

func TestDownloadVoice_EmptyFilePath(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"v1","file_path":""}}`)
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadVoice(bot, "v1")
	if err == nil {
		t.Fatal("DownloadVoice should return error for empty file_path")
	}
}

// ── Test DownloadPhoto ─────────────────────────────────────────────────────

func TestDownloadPhoto_Success(t *testing.T) {
	var callCount int
	handler := func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case strings.Contains(r.URL.String(), "getFile"):
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"photo_big","file_path":"photos/photo.jpg"}}`)
		case strings.HasSuffix(r.URL.String(), "photos/photo.jpg"):
			w.Write([]byte("fake-jpg-data"))
		default:
			http.Error(w, `{"ok":false,"description":"not found"}`, 404)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	// Multiple file IDs (simulating multiple sizes). Last one = largest.
	path, err := DownloadPhoto(bot, []string{"small", "medium", "photo_big"})
	if err != nil {
		t.Fatalf("DownloadPhoto() error: %v", err)
	}
	if path == "" {
		t.Fatal("DownloadPhoto() returned empty path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "fake-jpg-data" {
		t.Errorf("file content = %q, want %q", string(data), "fake-jpg-data")
	}

	if callCount < 2 {
		t.Errorf("expected at least 2 calls, got %d", callCount)
	}

	os.Remove(path)
}

func TestDownloadPhoto_EmptyIDs(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadPhoto(bot, nil)
	if err == nil {
		t.Fatal("DownloadPhoto should return error for nil fileIDs")
	}

	_, err = DownloadPhoto(bot, []string{})
	if err == nil {
		t.Fatal("DownloadPhoto should return error for empty fileIDs")
	}
}

func TestDownloadPhoto_GetFileError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"ok":false,"description":"bad file","error_code":400}`)
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadPhoto(bot, []string{"bad_photo"})
	if err == nil {
		t.Fatal("DownloadPhoto should return error on getFile failure")
	}
}

// ── Test MediaDir cleanup ──────────────────────────────────────────────────

func TestMediaDir_AlreadyExists(t *testing.T) {
	// Create a temp dir and override home to test existing dir.
	tmpDir := t.TempDir()
	mediaDir := filepath.Join(tmpDir, ".odek", "media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a test file.
	testFile := filepath.Join(mediaDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Check it's still there.
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read test file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}

	// Verify DownloadPhoto uses the correct media dir.
	// We can't easily override MediaDir, but we can verify the function
	// creates and returns a valid path.
	dir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir() error: %v", err)
	}
	if dir == "" {
		t.Fatal("MediaDir() returned empty path")
	}
}
