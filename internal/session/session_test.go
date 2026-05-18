package session

import (
	"fmt"
	"os"
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

// newTestStore creates a Store with a temp directory (isolated from ~/.kode/).
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
