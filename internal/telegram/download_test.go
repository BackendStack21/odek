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

	path, err := DownloadVoice(bot, 42, "voice123")
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

	_, err := DownloadVoice(bot, 42, "bad_id")
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

	_, err := DownloadVoice(bot, 42, "v1")
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
	path, err := DownloadPhoto(bot, 42, []string{"small", "medium", "photo_big"})
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

	_, err := DownloadPhoto(bot, 42, nil)
	if err == nil {
		t.Fatal("DownloadPhoto should return error for nil fileIDs")
	}

	_, err = DownloadPhoto(bot, 42, []string{})
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

	_, err := DownloadPhoto(bot, 42, []string{"bad_photo"})
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

// ── Error path tests ───────────────────────────────────────────────────

func TestMediaDir_MkdirAllError(t *testing.T) {
	tmp := t.TempDir()
	// Create a file where .odek/media/ should be, so MkdirAll fails.
	odekDir := filepath.Join(tmp, ".odek")
	if err := os.MkdirAll(odekDir, 0755); err != nil {
		t.Fatalf("mkdir .odek: %v", err)
	}
	if err := os.WriteFile(filepath.Join(odekDir, "media"), []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, err := MediaDir()
	if err == nil {
		t.Fatal("expected error when media path is blocked by a file")
	}
}

func TestDownloadVoice_DownloadFileError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"v1","file_path":"voice/clip.oga"}}`)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadVoice(bot, 42, "v1")
	if err == nil {
		t.Fatal("expected error when download fails")
	}
}

func TestDownloadVoice_EmptyExtensionFallback(t *testing.T) {
	var callCount int
	handler := func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case strings.Contains(r.URL.String(), "getFile"):
			// File path with no extension → should default to .ogg
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"v1","file_path":"voicedata"}}`)
		default:
			w.Write([]byte("audio-data"))
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadVoice(bot, 42, "v1")
	if err != nil {
		t.Fatalf("DownloadVoice error: %v", err)
	}
	if !strings.HasSuffix(path, ".ogg") {
		t.Errorf("expected .ogg fallback extension, got %q", path)
	}
	os.Remove(path)
}

func TestDownloadVoice_ShortFileIDSuffix(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"short","file_path":"voice/clip.ogg"}}`)
		} else {
			w.Write([]byte("data"))
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadVoice(bot, 42, "short")
	if err != nil {
		t.Fatalf("DownloadVoice error: %v", err)
	}
	// Filenames are now derived from a hash of the full fileID, not the raw id.
	if !strings.Contains(path, "voice_chat42_"+fileIDSuffix("short")) {
		t.Errorf("expected hashed fileID suffix in path, got %q", path)
	}
	os.Remove(path)
}

func TestDownloadPhoto_EmptyExtensionFallback(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"p1","file_path":"photodata"}}`)
		} else {
			w.Write([]byte("jpg-data"))
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadPhoto(bot, 42, []string{"p1"})
	if err != nil {
		t.Fatalf("DownloadPhoto error: %v", err)
	}
	if !strings.HasSuffix(path, ".jpg") {
		t.Errorf("expected .jpg fallback extension, got %q", path)
	}
	os.Remove(path)
}

func TestDownloadPhoto_DownloadFileError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"p1","file_path":"photos/img.jpg"}}`)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadPhoto(bot, 42, []string{"p1"})
	if err == nil {
		t.Fatal("expected error when download fails")
	}
}

func TestDownloadPhoto_FilePathEmpty(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"p1","file_path":""}}`)
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	_, err := DownloadPhoto(bot, 42, []string{"p1"})
	if err == nil {
		t.Fatal("expected error for empty file_path")
	}
}

func TestDownloadVoice_HashedFileIDSuffix(t *testing.T) {
	// A long fileID is hashed into a 16-hex-char suffix, not raw-truncated.
	longID := "abcdefghijklmnopqrstuvwxyz1234567890"
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"%s","file_path":"voice/clip.oga"}}`, longID)
		} else {
			w.Write([]byte("data"))
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadVoice(bot, 42, longID)
	if err != nil {
		t.Fatalf("DownloadVoice error: %v", err)
	}
	if !strings.Contains(path, "voice_chat42_"+fileIDSuffix(longID)) {
		t.Errorf("expected hashed fileID suffix in path, got %q", path)
	}
	// The raw id prefix must NOT appear — that was the collision bug.
	if strings.Contains(filepath.Base(path), longID[:16]) {
		t.Errorf("filename still contains raw fileID prefix: %q", path)
	}
	os.Remove(path)
}

func TestDownloadPhoto_HashedFileIDSuffix(t *testing.T) {
	longID := "abcdefghijklmnopqrstuvwxyz1234567890"
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"%s","file_path":"photos/img.jpg"}}`, longID)
		} else {
			w.Write([]byte("data"))
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	path, err := DownloadPhoto(bot, 42, []string{longID})
	if err != nil {
		t.Fatalf("DownloadPhoto error: %v", err)
	}
	if !strings.Contains(path, "photo_chat42_"+fileIDSuffix(longID)) {
		t.Errorf("expected hashed fileID suffix in path, got %q", path)
	}
	os.Remove(path)
}

// TestDownloadPhoto_PrefixCollisionAvoided is the regression test for the bug
// where two distinct Telegram photos sharing the long common file_id prefix
// (e.g. "AgACAgIAAxkBAAI…") were truncated to the same 16-char name and thus
// overwrote each other — making the bot report "image already processed".
func TestDownloadPhoto_PrefixCollisionAvoided(t *testing.T) {
	// Two different IDs that share the first 20 characters.
	idA := "AgACAgIAAxkBAAIvAAAA_distinct_A"
	idB := "AgACAgIAAxkBAAIvAAAA_distinct_B"
	if idA[:20] != idB[:20] {
		t.Fatalf("test setup: ids must share a prefix")
	}

	makeBot := func(id, body string) *Bot {
		handler := func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.String(), "getFile") {
				fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"%s","file_path":"photos/img.jpg"}}`, id)
			} else {
				w.Write([]byte(body))
			}
		}
		ts := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(ts.Close)
		return testBot(t, ts)
	}

	pathA, err := DownloadPhoto(makeBot(idA, "imageA"), 42, []string{idA})
	if err != nil {
		t.Fatalf("DownloadPhoto(A) error: %v", err)
	}
	defer os.Remove(pathA)
	pathB, err := DownloadPhoto(makeBot(idB, "imageB"), 42, []string{idB})
	if err != nil {
		t.Fatalf("DownloadPhoto(B) error: %v", err)
	}
	defer os.Remove(pathB)

	if pathA == pathB {
		t.Fatalf("distinct photos collided to the same filename: %q", pathA)
	}
}

func TestDownloadVoice_MediaDirError(t *testing.T) {
	// Set HOME to a path that can't have .odek/media created.
	tmp := t.TempDir()
	odekDir := filepath.Join(tmp, ".odek")
	if err := os.MkdirAll(odekDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Place a file where media/ should be.
	if err := os.WriteFile(filepath.Join(odekDir, "media"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, err := DownloadVoice(&Bot{BaseURL: "http://example.com"}, 42, "fid")
	if err == nil {
		t.Fatal("expected error when MediaDir fails")
	}
}

func TestDownloadPhoto_MediaDirError(t *testing.T) {
	tmp := t.TempDir()
	odekDir := filepath.Join(tmp, ".odek")
	if err := os.MkdirAll(odekDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(odekDir, "media"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", tmp)

	_, err := DownloadPhoto(&Bot{BaseURL: "http://example.com"}, 42, []string{"fid"})
	if err == nil {
		t.Fatal("expected error when MediaDir fails")
	}
}

// ── Test sanitizeDocName (path-traversal hardening) ─────────────────────────

func TestSanitizeDocName(t *testing.T) {
	const fileID = "AgACAgIAAxkBAAIabcdef0123456789"
	tests := []struct {
		name     string
		fileName string
		want     string
	}{
		{"plain name", "report.pdf", "report.pdf"},
		{"parent traversal", "../../.ssh/authorized_keys", "authorized_keys"},
		{"absolute path", "/etc/passwd", "passwd"},
		{"deep traversal to config", "../../../home/user/.odek/config.json", "config.json"},
		{"nested subdir", "a/b/c.txt", "c.txt"},
		{"dot-dot only", "..", "doc_" + fileID[:16] + ".bin"},
		{"empty falls back", "", "doc_" + fileID[:16] + ".bin"},
		{"hidden file", ".bashrc", "doc_" + fileID[:16] + ".bin"},
		{"hidden with traversal", "../../.ssh/.bashrc", "doc_" + fileID[:16] + ".bin"},
		{"unsafe chars replaced", "report (final) v2!.pdf", "report__final__v2_.pdf"},
		{"unicode replaced", "日本語.pdf", "___.pdf"},
		{"long name truncated", strings.Repeat("a", 300) + ".pdf", strings.Repeat("a", 196) + ".pdf"},
		{"only extension preserved", ".", "doc_" + fileID[:16] + ".bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeDocName(tt.fileName, fileID, "documents/file.bin")
			if got != tt.want {
				t.Errorf("sanitizeDocName(%q) = %q, want %q", tt.fileName, got, tt.want)
			}
			// The result must never contain a path separator.
			if strings.ContainsRune(got, filepath.Separator) {
				t.Errorf("sanitizeDocName(%q) = %q contains a path separator", tt.fileName, got)
			}
		})
	}
}

// TestDownloadDocument_NoTraversal verifies the full download path keeps a
// malicious filename confined to the media directory.
func TestDownloadDocument_NoTraversal(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.String(), "getFile"):
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"doc123","file_path":"documents/file.bin"}}`)
		case strings.HasSuffix(r.URL.String(), "documents/file.bin"):
			w.Write([]byte("pwned"))
		default:
			http.Error(w, `{"ok":false}`, 404)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)

	dir, err := MediaDir()
	if err != nil {
		t.Fatalf("MediaDir: %v", err)
	}
	defer os.RemoveAll(dir)

	localPath, err := DownloadDocument(bot, 42, "doc123", "../../../evil.txt")
	if err != nil {
		t.Fatalf("DownloadDocument: %v", err)
	}
	if filepath.Dir(localPath) != filepath.Clean(dir) {
		t.Errorf("file written to %q, want it inside %q", localPath, dir)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Error("traversal succeeded: evil.txt written outside media dir")
	}
}

// ── Download limits ────────────────────────────────────────────────────────

func TestDownloadVoice_MaxSizeExceeded(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"v1","file_path":"voice/file.ogg"}}`)
			return
		}
		w.Write([]byte("this payload is larger than five bytes"))
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)
	bot.MaxDownloadSize = 5

	_, err := DownloadVoice(bot, 42, "v1")
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("expected size-exceeded error, got %v", err)
	}
}

func TestDownloadVoice_QuotaExceeded(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"v1","file_path":"voice/file.ogg"}}`)
			return
		}
		w.Write([]byte("hello"))
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)
	bot.MediaQuotaPerChat = 3 // bytes — a 5-byte file should exceed it

	t.Setenv("HOME", t.TempDir())

	_, err := DownloadVoice(bot, 42, "v1")
	if err == nil || !strings.Contains(err.Error(), "media quota exceeded") {
		t.Fatalf("expected quota-exceeded error, got %v", err)
	}
}

// TestDownloadDocument_CountsTowardQuota is a regression test: documents must
// be named with the same "<type>_chat<id>_" prefix as voice/photo so they are
// matched by chatMediaPattern and counted toward the per-chat media quota.
// A bare "chat<id>_" prefix did not match the leading-underscore glob, letting
// documents bypass the cap entirely.
func TestDownloadDocument_CountsTowardQuota(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "getFile") {
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"d1","file_path":"documents/file.bin"}}`)
			return
		}
		w.Write([]byte("hello")) // 5 bytes
	}
	ts := httptest.NewServer(http.HandlerFunc(handler))
	defer ts.Close()
	bot := testBot(t, ts)
	bot.MediaQuotaPerChat = 8 // bytes — fits one 5-byte file, not two
	t.Setenv("HOME", t.TempDir())

	if _, err := DownloadDocument(bot, 42, "d1", "report.bin"); err != nil {
		t.Fatalf("first document download should fit under quota: %v", err)
	}
	// The first document (5 bytes) must count as existing usage, so a second
	// 5-byte document exceeds the 8-byte quota.
	_, err := DownloadDocument(bot, 42, "d2", "report2.bin")
	if err == nil || !strings.Contains(err.Error(), "media quota exceeded") {
		t.Fatalf("expected second document to exceed quota (first must be counted), got %v", err)
	}
}
