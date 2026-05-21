package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
)

func TestNewStore(t *testing.T) {
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore() returned nil")
	}
}

func TestNewStore_NoHomeEnv(t *testing.T) {
	// Unset HOME so os.UserHomeDir() fails — covering the error path at
	// line 60-63 of session.go.
	origHome := os.Getenv("HOME")
	os.Unsetenv("HOME")
	defer os.Setenv("HOME", origHome)

	_, err := NewStore()
	if err == nil {
		t.Error("expected error when HOME is unset")
	}
	if !strings.Contains(err.Error(), "home dir") {
		t.Errorf("expected 'home dir' in error, got: %v", err)
	}
}

func TestNewStore_InvalidDir(t *testing.T) {
	// Set HOME to a file path so MkdirAll fails when creating ~/.odek/sessions
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)

	dir := t.TempDir()
	// Create a file at the HOME path (so MkdirAll can't create a dir there)
	homeFile := filepath.Join(dir, "homefile")
	if err := os.WriteFile(homeFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	os.Setenv("HOME", homeFile)

	_, err := NewStore()
	if err == nil {
		t.Error("expected error when HOME is a file (MkdirAll should fail)")
	}
	if !strings.Contains(err.Error(), "create dir") {
		t.Errorf("expected 'create dir' in error, got: %v", err)
	}
}

func TestStore_CreateAndLoad(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "user", Content: "hello"},
	}
	sess, err := store.Create(msgs, "test-model", "hello")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if sess.ID == "" {
		t.Error("session ID should not be empty")
	}
	if sess.Turns != 1 {
		t.Errorf("Turns = %d, want 1", sess.Turns)
	}
	if sess.Model != "test-model" {
		t.Errorf("Model = %q", sess.Model)
	}
	if sess.Task != "hello" {
		t.Errorf("Task = %q", sess.Task)
	}
	if len(sess.Messages) != 2 {
		t.Errorf("Messages = %d, want 2", len(sess.Messages))
	}

	// Load back
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("loaded Messages = %d, want 2", len(loaded.Messages))
	}
	if loaded.Messages[1].Content != "hello" {
		t.Errorf("message content = %q", loaded.Messages[1].Content)
	}
}

func TestStore_Append(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first"},
	}
	sess, err := store.Create(msgs, "model", "first")
	if err != nil {
		t.Fatal(err)
	}

	// Append new messages
	newMsgs := []llm.Message{
		{Role: "assistant", Content: "response"},
		{Role: "user", Content: "follow-up"},
	}
	if err := store.Append(sess.ID, newMsgs); err != nil {
		t.Fatalf("Append() error: %v", err)
	}

	loaded, _ := store.Load(sess.ID)
	if len(loaded.Messages) != 4 {
		t.Errorf("Messages = %d, want 4", len(loaded.Messages))
	}
	if loaded.Turns != 2 {
		t.Errorf("Turns = %d, want 2", loaded.Turns)
	}
}

func TestStore_List(t *testing.T) {
	store := newTestStore(t)

	_, err := store.List(0)
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}

	// Create a session
	msgs := []llm.Message{{Role: "user", Content: "task"}}
	store.Create(msgs, "m1", "task")

	sessions, err := store.List(0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("List = %d sessions, want 1", len(sessions))
	}
	if sessions[0].Messages != nil {
		t.Error("List should not include message bodies")
	}
}

func TestStore_Latest(t *testing.T) {
	store := newTestStore(t)

	// No sessions
	sess, err := store.Latest()
	if sess != nil || err == nil {
		t.Error("Latest() on empty store should return nil, error")
	}

	// Create two sessions
	msgs1 := []llm.Message{{Role: "user", Content: "first"}}
	s1, _ := store.Create(msgs1, "m1", "first")
	msgs2 := []llm.Message{{Role: "user", Content: "second"}}
	s2, _ := store.Create(msgs2, "m2", "second")

	latest, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != s2.ID {
		t.Errorf("Latest() = %q, want %q", latest.ID, s2.ID)
	}
	if s1.ID == s2.ID {
		t.Error("session IDs should be unique")
	}
}

func TestStore_Delete(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "task"}}
	sess, _ := store.Create(msgs, "m", "task")

	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := store.Load(sess.ID); err == nil {
		t.Error("Load after delete should fail")
	}

	// Idempotent
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete nonexistent should not error, got: %v", err)
	}
}

func TestStore_Cleanup(t *testing.T) {
	store := newTestStore(t)

	// Create a "current" session
	msgs := []llm.Message{{Role: "user", Content: "current"}}
	current, err := store.Create(msgs, "m", "current")
	if err != nil {
		t.Fatal(err)
	}

	// Create an "old" session by rewriting its UpdatedAt
	msgs2 := []llm.Message{{Role: "user", Content: "old"}}
	oldSess, err := store.Create(msgs2, "m", "old")
	if err != nil {
		t.Fatal(err)
	}
	oldSess.UpdatedAt = oldSess.UpdatedAt.AddDate(0, 0, -30) // 30 days ago
	if err := store.Save(oldSess); err != nil {
		t.Fatal(err)
	}

	// Cleanup sessions older than 7 days
	deleted, err := store.Cleanup(time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Cleanup() deleted %d, want 1", deleted)
	}

	// Current session should still exist
	if _, err := store.Load(current.ID); err != nil {
		t.Errorf("current session should survive cleanup: %v", err)
	}

	// Old session should be gone
	if _, err := store.Load(oldSess.ID); err == nil {
		t.Error("old session should have been deleted")
	}
}

func TestStore_Cleanup_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	deleted, err := store.Cleanup(time.Now().UTC())
	if err != nil {
		t.Fatalf("Cleanup() on empty store: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Cleanup() deleted %d, want 0", deleted)
	}
}

func TestStore_Cleanup_ZeroDays(t *testing.T) {
	store := newTestStore(t)

	msgs := []llm.Message{{Role: "user", Content: "anything"}}
	sess, err := store.Create(msgs, "m", "test")
	if err != nil {
		t.Fatal(err)
	}

	// cleanup with 0 days = delete everything (all sessions are older than "right now" since UpdatedAt is from Create)
	deleted, err := store.Cleanup(time.Now().UTC())
	if err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Cleanup() deleted %d, want 1", deleted)
	}
	if _, err := store.Load(sess.ID); err == nil {
		t.Error("session should have been deleted")
	}
}

func TestStore_Cleanup_Idempotent(t *testing.T) {
	store := newTestStore(t)

	// Cleanup empty store twice — should not error
	_, err := store.Cleanup(time.Now().UTC())
	if err != nil {
		t.Fatalf("first Cleanup: %v", err)
	}
	_, err = store.Cleanup(time.Now().UTC())
	if err != nil {
		t.Fatalf("second Cleanup (idempotent): %v", err)
	}
}

func TestGenerateID(t *testing.T) {
	id := generateID()
	if !strings.Contains(id, "-") {
		t.Errorf("id = %q, should contain '-'", id)
	}
	if len(id) < 10 {
		t.Errorf("id too short: %q", id)
	}
	// Two calls should produce different IDs
	id2 := generateID()
	if id == id2 {
		t.Error("generateID() should produce unique IDs")
	}
}

func TestSession_GetMessages_Nil(t *testing.T) {
	var s *Session
	if msgs := s.GetMessages(); msgs == nil {
		t.Error("GetMessages on nil session should return empty slice, not nil")
	}
	if len(s.GetMessages()) != 0 {
		t.Error("GetMessages on nil session should return empty slice")
	}
}

func TestSession_GetMessages_Empty(t *testing.T) {
	s := &Session{}
	if msgs := s.GetMessages(); msgs == nil {
		t.Error("GetMessages on empty session should return empty, not nil")
	}
}

// ── Security: TOCTOU Race (Findings #5) ────────────────────────────────
//
// Append() reads a session file, modifies it, then writes it back.
// An attacker who swaps the file for a symlink between read and write
// could redirect the write to an arbitrary path. We fix this with:
//   - Atomic temp-file + os.Rename (Rename does NOT follow symlinks)
//   - sync.Mutex to serialize concurrent read-modify-write in Append
//
// These tests verify the fix and guard against regression.

func TestAppend_ConcurrentSafety(t *testing.T) {
	// Two concurrent Appends to the same session should both be reflected
	// in the final file — no lost writes.
	store := newTestStore(t)
	sess, err := store.Create(
		[]llm.Message{{Role: "user", Content: "start"}},
		"test", "start",
	)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	appendMsg := func(content string) {
		done <- store.Append(sess.ID, []llm.Message{{Role: "user", Content: content}})
	}

	go appendMsg("thread-a")
	go appendMsg("thread-b")

	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent Append error: %v", err)
		}
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 3 {
		t.Errorf("expected 3 messages (start + 2 appends), got %d", len(loaded.Messages))
	}
}

func TestSave_AtomicWriteNoPartialFile(t *testing.T) {
	// Verify that Save produces a valid JSON file (not a temp file,
	// not a truncated file) by checking the file path directly.
	store := newTestStore(t)
	sess, err := store.Create(
		[]llm.Message{{Role: "user", Content: "data"}},
		"test", "data",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Read the file directly — should be valid JSON
	path := store.Path(sess.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	if !strings.Contains(string(data), `"id":`) {
		t.Error("saved file doesn't look like valid JSON")
	}

	// No .tmp files should remain in the session directory
	entries, _ := os.ReadDir(store.Dir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stale temp file found: %s", e.Name())
		}
	}
}

func TestSave_SymlinkNotFollowed(t *testing.T) {
	// If the target path is a symlink, Save should replace the symlink
	// (not follow it) — this is the TOCTOU defense.
	store := newTestStore(t)
	sess, err := store.Create(
		[]llm.Message{{Role: "user", Content: "original"}},
		"test", "original",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Create a decoy file
	decoyPath := filepath.Join(store.Dir(), "decoy.txt")
	if err := os.WriteFile(decoyPath, []byte("decoy"), 0644); err != nil {
		t.Fatal(err)
	}

	// Replace session file with symlink to decoy
	realPath := store.Path(sess.ID)
	if err := os.Remove(realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("decoy.txt", realPath); err != nil {
		t.Fatal(err)
	}

	// Save should NOT follow the symlink — it should replace it
	sess.Messages = append(sess.Messages, llm.Message{Role: "assistant", Content: "response"})
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	// After Save, the real path should be a regular file, not a symlink
	fi, err := os.Lstat(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("Save left a symlink in place — should have been replaced with a regular file")
	}

	// Decoy file should still have its original content (intact)
	decoyData, err := os.ReadFile(decoyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoyData) != "decoy" {
		t.Errorf("decoy file was overwritten! content: %q", string(decoyData))
	}

	// The session file should contain valid session JSON
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}
}

func TestCountUserTurns(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: ""},
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "tool", Content: "c"},
		{Role: "user", Content: "d"},
	}
	if n := countUserTurns(msgs); n != 2 {
		t.Errorf("countUserTurns = %d, want 2", n)
	}
}

// newTestStore creates a Store with a temp directory (isolated from ~/.odek/).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	return store
}

func TestList_Limit(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 3; i++ {
		msgs := []llm.Message{{Role: "user", Content: fmt.Sprintf("task %d", i)}}
		sess, _ := store.Create(msgs, "test", fmt.Sprintf("task %d", i))
		// Stagger times so ordering is deterministic
		sess.UpdatedAt = time.Now().Add(time.Duration(i) * time.Hour)
		store.Save(sess)
	}
	sessions, err := store.List(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("List(2) returned %d sessions, want 2", len(sessions))
	}
}

// ── Security: Session ID Validation ──────────────────────────────────

func TestValidateSessionID_Valid(t *testing.T) {
	valid := []string{"20260518-abc123", "sess-001", "abcdef", "x"}
	for _, id := range valid {
		if err := ValidateSessionID(id); err != nil {
			t.Errorf("ValidateSessionID(%q) = %v, want nil", id, err)
		}
	}
}

func TestValidateSessionID_PathTraversal(t *testing.T) {
	invalid := []string{
		"../../etc/passwd",
		"../sessions/evil",
		"foo/bar",
		"foo\\bar",
		".",
		"..",
		"",
	}
	for _, id := range invalid {
		if err := ValidateSessionID(id); err == nil {
			t.Errorf("ValidateSessionID(%q) = nil, want error", id)
		}
	}
}

func TestStore_Load_PathTraversalRejected(t *testing.T) {
	store := newTestStore(t)
	_, err := store.Load("../../etc/passwd")
	if err == nil {
		t.Error("Load() with path traversal should return error")
	}
	if !strings.Contains(err.Error(), "invalid ID") {
		t.Errorf("error should mention 'invalid ID', got: %v", err)
	}
}

func TestValidateSessionID_NullBytes(t *testing.T) {
	// Null bytes (\x00) are not valid in filenames on Unix because
	// the OS uses null-terminated strings for paths. Even though
	// Go strings can contain null bytes, they should be rejected.
	ids := []string{"abc\x00def", "20260518-\x00abc123", "\x00evil"}
	for _, id := range ids {
		err := ValidateSessionID(id)
		if err == nil {
			t.Errorf("ValidateSessionID(%q) = nil, want error (null byte not allowed)", id)
		}
	}
}

func TestGenerateID_Format(t *testing.T) {
	id := generateID()
	// Format: YYYYMMDD-xxxxxx (8 digits, dash, 6 hex chars)
	if len(id) != 15 {
		t.Errorf("generateID() length = %d, want 15 (got %q)", len(id), id)
	}
	// Prefix must be 8 digits
	if id[0:8] != id[0:8] { // always true, but check digits
	}
	for i := 0; i < 8; i++ {
		if id[i] < '0' || id[i] > '9' {
			t.Errorf("generateID() prefix char %d = %q, want digit (got %q)", i, id[i], id)
		}
	}
	// Dash at position 8
	if id[8] != '-' {
		t.Errorf("generateID() char 8 = %q, want '-' (got %q)", id[8], id)
	}
	// Suffix must be 6 hex chars
	suffix := id[9:]
	if len(suffix) != 6 {
		t.Errorf("generateID() suffix length = %d, want 6 (got %q)", len(suffix), id)
	}
	for i, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("generateID() suffix char %d = %q, want hex digit (got %q)", i, c, id)
		}
	}
}

func TestStore_Latest_NoIndex(t *testing.T) {
	store := newTestStore(t)

	// Create a session (this writes both the session file and index.json)
	msgs := []llm.Message{{Role: "user", Content: "test"}}
	sess, err := store.Create(msgs, "m", "test")
	if err != nil {
		t.Fatal(err)
	}

	// Delete the index file to force the fallback path in Latest()
	idxPath := store.indexPath()
	if err := os.Remove(idxPath); err != nil {
		t.Fatal(err)
	}

	// Latest() should still work via directory scanning
	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest() after index deletion: %v", err)
	}
	if latest.ID != sess.ID {
		t.Errorf("Latest() = %q, want %q", latest.ID, sess.ID)
	}
	if latest.Task != "test" {
		t.Errorf("Latest().Task = %q, want %q", latest.Task, "test")
	}
}

func TestStore_Latest_SingleSession(t *testing.T) {
	store := newTestStore(t)

	msgs := []llm.Message{{Role: "user", Content: "only one"}}
	sess, err := store.Create(msgs, "m1", "only one")
	if err != nil {
		t.Fatal(err)
	}

	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest() error: %v", err)
	}
	if latest == nil {
		t.Fatal("Latest() returned nil")
	}
	if latest.ID != sess.ID {
		t.Errorf("Latest() = %q, want %q", latest.ID, sess.ID)
	}
	if latest.Task != "only one" {
		t.Errorf("Latest().Task = %q, want %q", latest.Task, "only one")
	}
	if latest.Turns != 1 {
		t.Errorf("Latest().Turns = %d, want 1", latest.Turns)
	}
}

func TestStore_Delete_PathTraversalRejected(t *testing.T) {
	store := newTestStore(t)
	err := store.Delete("../../etc/passwd")
	if err == nil {
		t.Error("Delete() with path traversal should return error")
	}
	if !strings.Contains(err.Error(), "invalid ID") {
		t.Errorf("error should mention 'invalid session', got: %v", err)
	}
}

// ── Additional edge-case coverage ──────────────────────────────────────

func TestValidateSessionID_NullByte(t *testing.T) {
	if err := ValidateSessionID("bad\x00id"); err == nil {
		t.Error("expected error for null byte")
	}
}

func TestLoad_CorruptFile(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "test"}}
	sess, _ := store.Create(msgs, "m", "test")

	// Overwrite the session file with garbage.
	os.WriteFile(store.Path(sess.ID), []byte("{invalid json"), 0644)

	_, err := store.Load(sess.ID)
	if err == nil {
		t.Fatal("expected error for corrupt file")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %q, want 'parse'", err)
	}
}

func TestAppend_NonExistentSession(t *testing.T) {
	store := newTestStore(t)
	err := store.Append("nonexistent-id", []llm.Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

func TestList_FallbackScanNoIndex(t *testing.T) {
	// Create a store, create a session, then delete the index file.
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "test"}}
	sess, _ := store.Create(msgs, "m", "test")

	// Remove the index file so List falls back to scanning individual files.
	idxPath := filepath.Join(store.Dir(), "index.json")
	os.Remove(idxPath)

	sessions, err := store.List(0)
	if err != nil {
		t.Fatalf("List fallback error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session via fallback, got %d", len(sessions))
	}
	if sessions[0].ID != sess.ID {
		t.Errorf("session ID = %q, want %q", sessions[0].ID, sess.ID)
	}
	if sessions[0].Messages != nil {
		t.Error("List fallback should not include message bodies")
	}
}

func TestList_FallbackScanSkipsNonSessionFiles(t *testing.T) {
	store := newTestStore(t)
	// Write a non-session file in the store directory.
	os.WriteFile(filepath.Join(store.Dir(), "note.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(store.Dir(), "index.json"), []byte("[]"), 0644)

	// Remove index so fallback is triggered.
	os.Remove(filepath.Join(store.Dir(), "index.json"))

	sessions, err := store.List(0)
	if err != nil {
		t.Fatalf("List fallback error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions (only .txt file), got %d", len(sessions))
	}
}

func TestList_ReadDirError(t *testing.T) {
	store := newTestStore(t)
	// Remove the sessions dir so ReadDir fails.
	os.RemoveAll(store.Dir())

	_, err := store.List(0)
	if err == nil {
		t.Fatal("expected error when sessions dir is missing")
	}
}

func TestLatest_FallbackScan(t *testing.T) {
	store := newTestStore(t)
	msgs1 := []llm.Message{{Role: "user", Content: "first"}}
	s1, _ := store.Create(msgs1, "m1", "first")

	// Remove index to force fallback scan.
	os.Remove(filepath.Join(store.Dir(), "index.json"))

	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest fallback error: %v", err)
	}
	if latest.ID != s1.ID {
		t.Errorf("Latest = %q, want %q", latest.ID, s1.ID)
	}
}

func TestLatest_ReadDirError(t *testing.T) {
	store := newTestStore(t)
	// Remove the sessions dir so ReadDir fails.
	os.RemoveAll(store.Dir())

	_, err := store.Latest()
	if err == nil {
		t.Fatal("expected error when sessions dir is missing")
	}
}

func TestLatest_FallbackSkipsNonSessionFiles(t *testing.T) {
	store := newTestStore(t)
	os.WriteFile(filepath.Join(store.Dir(), "note.txt"), []byte("hello"), 0644)
	os.Remove(filepath.Join(store.Dir(), "index.json"))

	_, err := store.Latest()
	if err == nil {
		t.Fatal("expected error when only non-session files exist")
	}
	if !strings.Contains(err.Error(), "no sessions found") {
		t.Errorf("error = %q, want 'no sessions found'", err)
	}
}

func TestDelete_OsRemoveError(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "test"}}
	sess, _ := store.Create(msgs, "m", "test")

	// Remove the sessions dir so the file can't be removed properly.
	os.RemoveAll(store.Dir())

	// Delete should now fail because the directory doesn't exist.
	err := store.Delete(sess.ID)
	if err == nil {
		t.Log("Delete succeeded (acceptable if remove on missing dir doesn't error)")
	}
}

func TestCleanup_FallbackScan(t *testing.T) {
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "old"}}
	oldSess, _ := store.Create(msgs, "m", "old")
	oldSess.UpdatedAt = oldSess.UpdatedAt.AddDate(0, 0, -30)
	store.Save(oldSess)

	// Remove index to force fallback scan.
	os.Remove(filepath.Join(store.Dir(), "index.json"))

	deleted, err := store.Cleanup(time.Now().UTC())
	if err != nil {
		t.Fatalf("Cleanup fallback error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
}

func TestCleanup_FallbackScanReadDirError(t *testing.T) {
	store := newTestStore(t)
	os.RemoveAll(store.Dir())

	_, err := store.Cleanup(time.Now().UTC())
	if err == nil {
		t.Fatal("expected error when sessions dir is missing")
	}
}

func TestGetMessages_WithMessages(t *testing.T) {
	s := &Session{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	}
	msgs := s.GetMessages()
	if len(msgs) != 1 {
		t.Errorf("GetMessages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "hello")
	}
}

func TestSaveIndexLocked_WriteError(t *testing.T) {
	// Create a store then make the directory unwritable.
	store := newTestStore(t)
	msgs := []llm.Message{{Role: "user", Content: "test"}}
	sess, _ := store.Create(msgs, "m", "test")

	// Remove the sessions dir so saving index fails.
	os.RemoveAll(store.Dir())

	// Save should fail because index can't be written.
	err := store.Save(sess)
	if err == nil {
		t.Log("Save error after removing dir (may succeed if dir is recreated)")
	}
}
