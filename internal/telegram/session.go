// Package telegram provides Telegram bot integration.
package telegram

import (
	"fmt"
	"sync"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/session"
)

// ── Types ──────────────────────────────────────────────────────────────

// SessionManager manages per-chat Telegram sessions backed by the existing
// session.Store. Each Telegram chat gets its own session identified by
// "tg-<chatID>". An in-memory cache avoids redundant disk reads.
type SessionManager struct {
	Store    *session.Store
	Cache    map[int64]*ChatSession
	Mu       sync.RWMutex
	BaseDir  string
	SessionTTL time.Duration
}

// ChatSession represents a single Telegram chat's agent conversation.
type ChatSession struct {
	ChatID     int64
	SessionID  string
	Messages   []llm.Message
	LastActive time.Time
	TurnCount  int
}

// ── Constructor ────────────────────────────────────────────────────────

// NewSessionManager creates a new SessionManager backed by the given store.
// The ttl parameter controls how long a session is considered active since
// its last use. If ttl is 0, a default of 24h is used.
// The cache map is initialized to empty.
func NewSessionManager(store *session.Store, ttl time.Duration) *SessionManager {
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &SessionManager{
		Store:      store,
		Cache:      make(map[int64]*ChatSession),
		SessionTTL: ttl,
	}
}

// ── Methods ────────────────────────────────────────────────────────────

// GetOrCreate returns the ChatSession for the given chatID.
// It checks the in-memory cache first; if the entry exists and hasn't
// expired (24h), it is returned directly. Otherwise a new ChatSession
// is created with a "tg-<chatID>" session ID, cached, and returned.
func (sm *SessionManager) GetOrCreate(chatID int64) (*ChatSession, error) {
	sm.Mu.RLock()
	cs, ok := sm.Cache[chatID]
	sm.Mu.RUnlock()

	if ok && time.Since(cs.LastActive) < sm.SessionTTL {
		return cs, nil
	}

	cs = &ChatSession{
		ChatID:     chatID,
		SessionID:  fmt.Sprintf("tg-%d", chatID),
		Messages:   make([]llm.Message, 0),
		LastActive: time.Now(),
		TurnCount:  0,
	}

	sm.Mu.Lock()
	sm.Cache[chatID] = cs
	sm.Mu.Unlock()

	return cs, nil
}

// Save persists the given messages for a chat session to both the cache
// and the backing session.Store. It updates LastActive, increments
// TurnCount, and writes a full session.Session to the store.
func (sm *SessionManager) Save(chatID int64, messages []llm.Message) error {
	sm.Mu.Lock()
	cs, ok := sm.Cache[chatID]
	if ok {
		cs.Messages = messages
		cs.LastActive = time.Now()
		cs.TurnCount++
	} else {
		cs = &ChatSession{
			ChatID:     chatID,
			SessionID:  fmt.Sprintf("tg-%d", chatID),
			Messages:   messages,
			LastActive: time.Now(),
			TurnCount:  1,
		}
		sm.Cache[chatID] = cs
	}
	sm.Mu.Unlock()

	now := time.Now()
	sess := &session.Session{
		ID:        cs.SessionID,
		CreatedAt: now,
		UpdatedAt: now,
		Model:     "",
		Turns:     cs.TurnCount,
		Task:      fmt.Sprintf("tg-%d", chatID),
		Messages:  messages,
	}

	return sm.Store.Save(sess)
}

// Load retrieves a ChatSession from the cache first, then from the
// backing store. If the session exists in the store but not in cache,
// it is loaded from disk, converted to a ChatSession, and cached.
// Returns nil, nil if the session is not found anywhere — callers
// should use GetOrCreate to create a new session in that case.
func (sm *SessionManager) Load(chatID int64) (*ChatSession, error) {
	sm.Mu.RLock()
	cs, ok := sm.Cache[chatID]
	sm.Mu.RUnlock()

	if ok {
		return cs, nil
	}

	sessionID := fmt.Sprintf("tg-%d", chatID)
	sess, err := sm.Store.Load(sessionID)
	if err != nil {
		return nil, nil
	}

	cs = &ChatSession{
		ChatID:     chatID,
		SessionID:  sess.ID,
		Messages:   sess.Messages,
		LastActive: sess.UpdatedAt,
		TurnCount:  sess.Turns,
	}

	sm.Mu.Lock()
	sm.Cache[chatID] = cs
	sm.Mu.Unlock()

	return cs, nil
}

// Delete removes the chat session from both the cache and the backing
// store. Idempotent — returns nil if the session doesn't exist.
func (sm *SessionManager) Delete(chatID int64) error {
	sessionID := fmt.Sprintf("tg-%d", chatID)

	sm.Mu.Lock()
	delete(sm.Cache, chatID)
	sm.Mu.Unlock()

	return sm.Store.Delete(sessionID)
}

// AppendMessage adds a single message (role + content) to the chat
// session's message list and saves the updated session. It uses
// GetOrCreate to ensure the session exists.
func (sm *SessionManager) AppendMessage(chatID int64, role string, content string) error {
	cs, err := sm.GetOrCreate(chatID)
	if err != nil {
		return err
	}

	cs.Messages = append(cs.Messages, llm.Message{Role: role, Content: content})
	return sm.Save(chatID, cs.Messages)
}
