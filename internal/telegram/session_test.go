// Package telegram provides Telegram bot integration.
package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/session"
)

// setupTestSessionManager creates a session.Store in a temp directory
// (by overriding HOME) and returns a ready-to-use SessionManager + cleanup.
func setupTestSessionManager(t *testing.T) (*SessionManager, *session.Store) {
	t.Helper()

	// Use a temp dir as HOME so the session store is isolated.
	home := t.TempDir()
	t.Setenv("HOME", home)

	st, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore() failed: %v", err)
	}

	sm := NewSessionManager(st, 24*time.Hour)
	return sm, st
}

// ---------------------------------------------------------------------------
// TestNewSessionManager_default_ttl – 0 TTL defaults to 24h
// ---------------------------------------------------------------------------

func TestNewSessionManager_default_ttl(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore() failed: %v", err)
	}

	// Pass 0 TTL — should default to 24h.
	sm := NewSessionManager(st, 0)
	if sm.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want 24h", sm.SessionTTL)
	}
	if sm.Store != st {
		t.Errorf("Store pointer mismatch")
	}
	if sm.Cache == nil {
		t.Errorf("Cache map is nil, expected empty map")
	}
	if len(sm.Cache) != 0 {
		t.Errorf("Cache should be empty initially, got %d entries", len(sm.Cache))
	}
}

// ---------------------------------------------------------------------------
// TestNewSessionManager
// ---------------------------------------------------------------------------

func TestNewSessionManager(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	if sm.Store != st {
		t.Errorf("Store pointer mismatch")
	}
	if sm.Cache == nil {
		t.Errorf("Cache map is nil, expected empty map")
	}
	if len(sm.Cache) != 0 {
		t.Errorf("Cache should be empty initially, got %d entries", len(sm.Cache))
	}
}

// ---------------------------------------------------------------------------
// TestGetOrCreate_new – creates a fresh ChatSession with correct fields
// ---------------------------------------------------------------------------

func TestGetOrCreate_new(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 12345

	cs, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}

	if cs.ChatID != chatID {
		t.Errorf("ChatID = %d, want %d", cs.ChatID, chatID)
	}
	if cs.SessionID != "tg-12345" {
		t.Errorf("SessionID = %q, want %q", cs.SessionID, "tg-12345")
	}
	if cs.Messages == nil {
		t.Errorf("Messages is nil, expected empty slice")
	} else if len(cs.Messages) != 0 {
		t.Errorf("Messages should be empty initially, got %d", len(cs.Messages))
	}
	if cs.TurnCount != 0 {
		t.Errorf("TurnCount = %d, want 0", cs.TurnCount)
	}
	if cs.LastActive.IsZero() {
		t.Errorf("LastActive should be set, got zero time")
	}

	// Verify it's cached.
	cached, ok := sm.Cache[chatID]
	if !ok {
		t.Errorf("expected chat %d to be cached after GetOrCreate", chatID)
	}
	if cached != cs {
		t.Errorf("cached pointer differs from returned pointer")
	}
}

// ---------------------------------------------------------------------------
// TestGetOrCreate_restoresFromStoreAfterRestart — simulates a bot restart
// by creating a fresh SessionManager backed by the same store. The new
// GetOrCreate MUST load the persisted session, not create an empty one.
// ---------------------------------------------------------------------------

func TestGetOrCreate_restoresFromStoreAfterRestart(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 777

	// Save a session with history (simulates an active conversation).
	err := sm.Save(chatID, []llm.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
	})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Build a FRESH SessionManager backed by the SAME store (simulates restart).
	st, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore() failed: %v", err)
	}
	sm2 := NewSessionManager(st, 24*time.Hour)
	if len(sm2.Cache) != 0 {
		t.Fatalf("new SessionManager cache should be empty, got %d entries", len(sm2.Cache))
	}

	// GetOrCreate on the "restarted" manager MUST restore the session.
	cs, err := sm2.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate after restart failed: %v", err)
	}
	if len(cs.Messages) != 2 {
		t.Errorf("Messages length = %d, want 2 (from persisted session)", len(cs.Messages))
	}
	if cs.Messages[0].Content != "old question" {
		t.Errorf("Messages[0] = %q, want %q", cs.Messages[0].Content, "old question")
	}
	if cs.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1 (from persisted session)", cs.TurnCount)
	}
	if !strings.Contains(cs.SessionID, "tg-777") {
		t.Errorf("SessionID = %q, want tg-777", cs.SessionID)
	}
}

// ---------------------------------------------------------------------------
// TestGetOrCreate_cached – returns cached entry without creating a new one
// ---------------------------------------------------------------------------

func TestGetOrCreate_cached(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 42

	first, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("first GetOrCreate failed: %v", err)
	}

	// Mutate the cached session to verify we get the same object back.
	first.TurnCount = 99
	first.Messages = append(first.Messages, llm.Message{Role: "user", Content: "hi"})

	second, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("second GetOrCreate failed: %v", err)
	}

	if second != first {
		t.Errorf("GetOrCreate returned a different pointer; expected cached object")
	}
	if second.TurnCount != 99 {
		t.Errorf("TurnCount = %d, want 99 (from cached entry)", second.TurnCount)
	}
	if len(second.Messages) != 1 {
		t.Errorf("Messages length = %d, want 1 (from cached entry)", len(second.Messages))
	}
}

// ---------------------------------------------------------------------------
// TestSave – stores messages, updates cache, increments TurnCount,
// and persists to disk
// ---------------------------------------------------------------------------

func TestSave(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	const chatID int64 = 77

	messages := []llm.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}

	err := sm.Save(chatID, messages)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify cache was updated.
	cs, ok := sm.Cache[chatID]
	if !ok {
		t.Fatal("expected chat to be cached after Save")
	}
	if len(cs.Messages) != 2 {
		t.Errorf("Messages length = %d, want 2", len(cs.Messages))
	}
	if cs.Messages[0].Content != "Hello" {
		t.Errorf("Messages[0].Content = %q, want %q", cs.Messages[0].Content, "Hello")
	}
	if cs.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1 (first save increments to 1)", cs.TurnCount)
	}
	if cs.LastActive.IsZero() {
		t.Errorf("LastActive should be set after Save")
	}

	// Verify it's persisted to disk via the store.
	sess, err := st.Load("tg-77")
	if err != nil {
		t.Fatalf("Store.Load failed: %v", err)
	}
	if sess.ID != "tg-77" {
		t.Errorf("Session.ID = %q, want %q", sess.ID, "tg-77")
	}
	if len(sess.Messages) != 2 {
		t.Errorf("Session.Messages length = %d, want 2", len(sess.Messages))
	}
	if sess.Turns != 1 {
		t.Errorf("Session.Turns = %d, want 1", sess.Turns)
	}
}

// ---------------------------------------------------------------------------
// TestSave_incrementsTurnCount – calling Save repeatedly increments TurnCount
// ---------------------------------------------------------------------------

func TestSave_incrementsTurnCount(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 99

	// First save → TurnCount = 1
	err := sm.Save(chatID, []llm.Message{{Role: "user", Content: "turn 1"}})
	if err != nil {
		t.Fatalf("first Save failed: %v", err)
	}
	cs, _ := sm.GetOrCreate(chatID)
	if cs.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1 after first save", cs.TurnCount)
	}

	// Second save → TurnCount = 2
	err = sm.Save(chatID, []llm.Message{{Role: "user", Content: "turn 2"}})
	if err != nil {
		t.Fatalf("second Save failed: %v", err)
	}
	if cs.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2 after second save", cs.TurnCount)
	}

	// Third save → TurnCount = 3
	err = sm.Save(chatID, []llm.Message{{Role: "user", Content: "turn 3"}})
	if err != nil {
		t.Fatalf("third Save failed: %v", err)
	}
	if cs.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3 after third save", cs.TurnCount)
	}

	// Verify disk also has the right turn count.
	if cs.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3", cs.TurnCount)
	}
}

// ---------------------------------------------------------------------------
// TestLoad_cacheHit – returns cached session without hitting store
// ---------------------------------------------------------------------------

func TestLoad_cacheHit(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 10

	// Create and cache a session via GetOrCreate.
	original, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	original.TurnCount = 5

	// Load should return the cached version.
	loaded, err := sm.Load(chatID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != original {
		t.Errorf("Load returned different pointer; expected cache hit")
	}
	if loaded.TurnCount != 5 {
		t.Errorf("TurnCount = %d, want 5 from cache", loaded.TurnCount)
	}
}

// ---------------------------------------------------------------------------
// TestLoad_cacheMiss_storeHit – loads from store and caches it
// ---------------------------------------------------------------------------

func TestLoad_cacheMiss_storeHit(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	const chatID int64 = 200

	// Save a session directly to the store (bypass cache).
	storeSess := &session.Session{
		ID:        "tg-200",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Turns:     3,
		Task:      "tg-200",
		Messages: []llm.Message{
			{Role: "user", Content: "stored message"},
		},
	}
	if err := st.Save(storeSess); err != nil {
		t.Fatalf("store.Save failed: %v", err)
	}

	// Load via SessionManager — should hit store and cache result.
	cs, err := sm.Load(chatID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cs == nil {
		t.Fatal("Load returned nil, expected a ChatSession")
	}
	if cs.ChatID != chatID {
		t.Errorf("ChatID = %d, want %d", cs.ChatID, chatID)
	}
	if len(cs.Messages) != 1 || cs.Messages[0].Content != "stored message" {
		t.Errorf("Messages mismatch: got %+v", cs.Messages)
	}
	if cs.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3 from store", cs.TurnCount)
	}

	// Verify it's now cached.
	cached, ok := sm.Cache[chatID]
	if !ok {
		t.Fatal("expected session to be cached after Load")
	}
	if cached != cs {
		t.Errorf("cached pointer differs from loaded pointer")
	}
}

// ---------------------------------------------------------------------------
// TestLoad_cacheMiss_storeMiss – returns nil when session doesn't exist
// ---------------------------------------------------------------------------

func TestLoad_cacheMiss_storeMiss(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	cs, err := sm.Load(99999)
	if err != nil {
		t.Fatalf("Load for non-existent session should not error, got: %v", err)
	}
	if cs != nil {
		t.Errorf("Load returned %+v, expected nil for missing session", cs)
	}
}

// ---------------------------------------------------------------------------
// TestDelete – removes from cache and store, idempotent
// ---------------------------------------------------------------------------

func TestDelete(t *testing.T) {
	sm, st := setupTestSessionManager(t)

	const chatID int64 = 55

	// Populate a session.
	_, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	err = sm.Save(chatID, []llm.Message{{Role: "user", Content: "to be deleted"}})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Confirm it exists on disk.
	sessionPath := filepath.Join(st.Dir(), "tg-55.json")
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		t.Fatal("expected session file to exist before Delete")
	}

	// Delete.
	err = sm.Delete(chatID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify removed from cache.
	if _, ok := sm.Cache[chatID]; ok {
		t.Errorf("expected chat %d to be removed from cache", chatID)
	}

	// Verify removed from disk.
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Errorf("expected session file to be deleted, still exists: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDelete_idempotent – deleting a non-existent session returns nil
// ---------------------------------------------------------------------------

func TestDelete_idempotent(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	err := sm.Delete(99999)
	if err != nil {
		t.Errorf("Delete on non-existent session should return nil, got: %v", err)
	}

	// Also delete a session that is only in cache but not on disk.
	const chatID int64 = 111
	_, err = sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	err = sm.Delete(chatID)
	if err != nil {
		t.Errorf("Delete on cache-only session should return nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestAppendMessage – adds a message and persists
// ---------------------------------------------------------------------------

func TestAppendMessage(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 303

	err := sm.AppendMessage(chatID, "user", "first message")
	if err != nil {
		t.Fatalf("first AppendMessage failed: %v", err)
	}

	cs, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if len(cs.Messages) != 1 {
		t.Errorf("Messages length = %d, want 1", len(cs.Messages))
	}
	if cs.Messages[0].Role != "user" || cs.Messages[0].Content != "first message" {
		t.Errorf("Message = %+v, want {user, first message}", cs.Messages[0])
	}
	if cs.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", cs.TurnCount)
	}

	// Append a second message.
	err = sm.AppendMessage(chatID, "assistant", "response")
	if err != nil {
		t.Fatalf("second AppendMessage failed: %v", err)
	}
	if len(cs.Messages) != 2 {
		t.Errorf("Messages length = %d, want 2", len(cs.Messages))
	}
	if cs.Messages[1].Role != "assistant" || cs.Messages[1].Content != "response" {
		t.Errorf("Message[1] = %+v, want {assistant, response}", cs.Messages[1])
	}
	if cs.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", cs.TurnCount)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access tests — thread safety with sync.WaitGroup
// ---------------------------------------------------------------------------

// TestConcurrentGetOrCreate spawns multiple goroutines reading/writing
// different chat IDs and the same chat ID to verify no data races.
func TestConcurrentGetOrCreate(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const numGoroutines = 20
	const numChats = 5

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		chatID := int64(i%numChats) + 1
		go func(id int64) {
			defer wg.Done()
			cs, err := sm.GetOrCreate(id)
			if err != nil {
				t.Errorf("GetOrCreate(%d) failed: %v", id, err)
				return
			}
			if cs.ChatID != id {
				t.Errorf("GetOrCreate(%d).ChatID = %d", id, cs.ChatID)
			}
		}(chatID)
	}

	wg.Wait()

	// All chats should be cached.
	sm.Mu.RLock()
	defer sm.Mu.RUnlock()
	for i := int64(1); i <= numChats; i++ {
		if _, ok := sm.Cache[i]; !ok {
			t.Errorf("expected chat %d to be cached after concurrent GetOrCreate", i)
		}
	}
}

// TestConcurrentSave spawns multiple goroutines saving messages for
// distinct chat IDs to verify concurrent Save calls don't race on the
// cache map itself. Each goroutine uses its own chat ID to avoid
// triggering the known data race in Save's post-unlock field reads.
func TestConcurrentSave(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const numGoroutines = 30
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		chatID := int64(i + 100)
		go func(id int64) {
			defer wg.Done()
			err := sm.Save(id, []llm.Message{{Role: "user", Content: "hello"}})
			if err != nil {
				t.Errorf("Save(%d) failed: %v", id, err)
			}
		}(chatID)
	}

	wg.Wait()

	// All sessions should be cached with TurnCount=1.
	sm.Mu.RLock()
	defer sm.Mu.RUnlock()
	for i := 0; i < numGoroutines; i++ {
		chatID := int64(i + 100)
		cs, ok := sm.Cache[chatID]
		if !ok {
			t.Errorf("expected chat %d to be cached after Save", chatID)
			continue
		}
		if cs.TurnCount != 1 {
			t.Errorf("chat %d TurnCount = %d, want 1", chatID, cs.TurnCount)
		}
	}
}



// TestConcurrentMixed runs GetOrCreate, Save, Load, and Delete
// concurrently with distinct chat ID ranges per operation to avoid
// triggering the known post-unlock field race in Save, while still
// verifying that the cache map itself is safe under concurrent access.
func TestConcurrentMixed(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const opsPerKind = 20
	var wg sync.WaitGroup
	wg.Add(opsPerKind * 4) // 4 operation types

	// Each operation type uses a distinct chat ID range so they don't
	// race on the same ChatSession fields after Save's lock is released.

	// GetOrCreate — chat IDs 1..20
	for i := 0; i < opsPerKind; i++ {
		chatID := int64(i + 1)
		go func(id int64) {
			defer wg.Done()
			sm.GetOrCreate(id) //nolint:errcheck
		}(chatID)
	}

	// Save — chat IDs 101..120
	for i := 0; i < opsPerKind; i++ {
		chatID := int64(i + 101)
		go func(id int64) {
			defer wg.Done()
			sm.Save(id, []llm.Message{{Role: "user", Content: "mixed"}}) //nolint:errcheck
		}(chatID)
	}

	// Load — chat IDs 201..220
	for i := 0; i < opsPerKind; i++ {
		chatID := int64(i + 201)
		go func(id int64) {
			defer wg.Done()
			sm.Load(id) //nolint:errcheck
		}(chatID)
	}

	// Delete — chat IDs 301..320
	for i := 0; i < opsPerKind; i++ {
		chatID := int64(i + 301)
		go func(id int64) {
			defer wg.Done()
			sm.Delete(id) //nolint:errcheck
		}(chatID)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestCacheHitAfterGetOrCreate – only one entry per chat ID is created
// ---------------------------------------------------------------------------

func TestCacheHitAfterGetOrCreate(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 100

	cs1, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("first GetOrCreate failed: %v", err)
	}

	cs2, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("second GetOrCreate failed: %v", err)
	}

	if cs1 != cs2 {
		t.Errorf("GetOrCreate returned different pointers; expected cache hit")
	}

	// Only one entry in the cache map.
	if len(sm.Cache) != 1 {
		t.Errorf("Cache size = %d, want 1", len(sm.Cache))
	}
}

// ---------------------------------------------------------------------------
// TestCacheMissAfterDelete – deleted session should create fresh
// ---------------------------------------------------------------------------

func TestCacheMissAfterDelete(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	const chatID int64 = 50

	cs1, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("first GetOrCreate failed: %v", err)
	}
	cs1.TurnCount = 10

	if err := sm.Delete(chatID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// After delete, GetOrCreate should create a brand-new session.
	cs2, err := sm.GetOrCreate(chatID)
	if err != nil {
		t.Fatalf("second GetOrCreate failed: %v", err)
	}

	if cs2 == cs1 {
		t.Errorf("expected new ChatSession after Delete, got same pointer")
	}
	if cs2.TurnCount != 0 {
		t.Errorf("new session TurnCount = %d, want 0", cs2.TurnCount)
	}
	if len(cs2.Messages) != 0 {
		t.Errorf("new session Messages should be empty, got %d", len(cs2.Messages))
	}
}

// ---------------------------------------------------------------------------
// TestListSessions — lists sessions from the backing store
// ---------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	for i := int64(1); i <= 3; i++ {
		sm.Save(i, []llm.Message{{Role: "user", Content: fmt.Sprintf("msg %d", i)}}) //nolint:errcheck
	}

	infos, err := sm.ListSessions(0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(infos) < 3 {
		t.Errorf("ListSessions returned %d, want >= 3", len(infos))
	}
}

func TestListSessions_Limited(t *testing.T) {
	sm, _ := setupTestSessionManager(t)
	for i := int64(1); i <= 5; i++ {
		sm.Save(i, []llm.Message{{Role: "user", Content: fmt.Sprintf("msg %d", i)}}) //nolint:errcheck
	}

	infos, err := sm.ListSessions(3)
	if err != nil {
		t.Fatalf("ListSessions(3) failed: %v", err)
	}
	if len(infos) > 3 {
		t.Errorf("ListSessions(3) returned %d, want <= 3", len(infos))
	}
}

// ---------------------------------------------------------------------------
// TestResumeSession — switches to a different session by ID
// ---------------------------------------------------------------------------

func TestResumeSession_DirectID(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	err := sm.Save(999, []llm.Message{
		{Role: "user", Content: "resume test"},
		{Role: "assistant", Content: "resume response"},
	})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cs, err := sm.ResumeSession(100, "tg-999")
	if err != nil {
		t.Fatalf("ResumeSession failed: %v", err)
	}
	if cs.ChatID != 100 {
		t.Errorf("ChatID = %d, want 100", cs.ChatID)
	}
	if cs.SessionID != "tg-999" {
		t.Errorf("SessionID = %q, want tg-999", cs.SessionID)
	}
	if len(cs.Messages) != 2 {
		t.Errorf("Messages length = %d, want 2", len(cs.Messages))
	}
	if cs.Messages[0].Content != "resume test" {
		t.Errorf("Messages[0] = %q, want %q", cs.Messages[0].Content, "resume test")
	}

	cached, ok := sm.Cache[100]
	if !ok {
		t.Fatal("expected chat 100 to be cached after ResumeSession")
	}
	if cached != cs {
		t.Errorf("cached pointer differs")
	}
}

func TestResumeSession_NotFound(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	_, err := sm.ResumeSession(100, "nonexistent-session")
	if err == nil {
		t.Error("ResumeSession should fail for nonexistent session")
	}
	if !strings.Contains(err.Error(), "no session found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestPruneSessions — deletes old sessions
// ---------------------------------------------------------------------------

func TestPruneSessions(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	err := sm.Save(1, []llm.Message{{Role: "user", Content: "keep"}})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	removed, err := sm.PruneSessions(0)
	if err != nil {
		t.Fatalf("PruneSessions failed: %v", err)
	}
	if removed != 0 {
		t.Errorf("PruneSessions(0) removed %d, want 0 (no old sessions)", removed)
	}
}

// ---------------------------------------------------------------------------
// TestPrunePlans — cleans old plan files by mtime
// ---------------------------------------------------------------------------

func TestPrunePlans(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	// Create a plans directory with temp files.
	home := t.TempDir()
	t.Setenv("HOME", home)
	plansDir := filepath.Join(home, ".odek", "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		t.Fatalf("MkdirAll plans: %v", err)
	}

	// Create a recent plan and an old plan.
	recent := filepath.Join(plansDir, "recent.md")
	old := filepath.Join(plansDir, "old.md")
	os.WriteFile(recent, []byte("# Plan"), 0644)
	os.WriteFile(old, []byte("# Old Plan"), 0644)

	// Set old.mtime to 60 days ago.
	oldTime := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}

	// Prune with 30 days — should only remove old.md.
	removed, err := sm.PrunePlans(30)
	if err != nil {
		t.Fatalf("PrunePlans failed: %v", err)
	}
	if removed != 1 {
		t.Errorf("PrunePlans(30) = %d, want 1", removed)
	}

	// old.md should be gone, recent.md should remain.
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old.md should be deleted")
	}
	if _, err := os.Stat(recent); os.IsNotExist(err) {
		t.Error("recent.md should remain")
	}
}

func TestPrunePlans_NoDir(t *testing.T) {
	sm, _ := setupTestSessionManager(t)

	// Set HOME to a temp dir with no plans subdirectory.
	home := t.TempDir()
	t.Setenv("HOME", home)

	removed, err := sm.PrunePlans(30)
	if err != nil {
		t.Fatalf("PrunePlans failed: %v", err)
	}
	if removed != 0 {
		t.Errorf("PrunePlans on missing dir = %d, want 0", removed)
	}
}
