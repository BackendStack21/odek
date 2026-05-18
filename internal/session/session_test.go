package session

import (
	"os"
	"strings"
	"testing"

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
