// Package telegram provides Telegram bot integration.
package telegram

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	Store          *session.Store
	Cache          map[int64]*ChatSession
	Mu             sync.RWMutex
	BaseDir        string
	SessionTTL     time.Duration
	clarifyChannels sync.Map // map[int64]chan string — per-chat clarify response channels
}

// ChatSession represents a single Telegram chat's agent conversation.
type ChatSession struct {
	ChatID     int64
	SessionID  string
	Messages   []llm.Message
	CreatedAt  time.Time
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
// Checks the in-memory cache first, then the backing session store,
// and only creates a new empty session as a last resort. This ensures
// conversations survive bot restarts without the user needing to ask
// for resume explicitly.
func (sm *SessionManager) GetOrCreate(chatID int64) (*ChatSession, error) {
	sm.Mu.RLock()
	cs, ok := sm.Cache[chatID]
	sm.Mu.RUnlock()

	if ok && time.Since(cs.LastActive) < sm.SessionTTL {
		return cs, nil
	}

	// Missed cache — try the backing store before creating fresh.
	loaded, err := sm.Load(chatID)
	if err != nil {
		// Store error (corrupt, permission, etc.) — log but don't
		// block the user. Create a fresh session instead.
		return nil, err
	}
	if loaded != nil {
		return loaded, nil
	}

	cs = &ChatSession{
		ChatID:     chatID,
		SessionID:  fmt.Sprintf("tg-%d", chatID),
		Messages:   make([]llm.Message, 0),
		CreatedAt:  time.Now(),
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
		// Copy-on-write: create a new ChatSession so existing pointers
		// held by Load() callers are not mutated, avoiding data races.
		updated := *cs
		updated.Messages = messages
		updated.LastActive = time.Now()
		updated.TurnCount++
		cs = &updated
		sm.Cache[chatID] = cs
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
	// Snapshot fields needed after unlock to avoid data race:
	sessionID := cs.SessionID
	createdAt := cs.CreatedAt
	turnCount := cs.TurnCount
	sm.Mu.Unlock()

	sess := &session.Session{
		ID:        sessionID,
		CreatedAt: createdAt,
		UpdatedAt: time.Now(),
		Model:     "",
		Turns:     turnCount,
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
		// File not found = expected, same as empty cache.
		// Use errors.Is to unwrap through %w-wrapped errors.
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("telegram: load session %d: %w", chatID, err)
	}

	cs = &ChatSession{
		ChatID:     chatID,
		SessionID:  sess.ID,
		Messages:   sess.Messages,
		CreatedAt:  sess.CreatedAt,
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

// ── Session Management ─────────────────────────────────────────────────

// ClarifyChannel methods manage per-chat response channels for the clarify
// tool. When the agent calls clarify, it blocks on a channel; the Telegram
// callback handler sends the user's response to unblock it.

// SetClarifyChannel stores a clarify response channel for the given chat.
func (sm *SessionManager) SetClarifyChannel(chatID int64, ch chan string) {
	sm.clarifyChannels.Store(chatID, ch)
}

// GetClarifyChannel retrieves the clarify response channel for a chat.
// Returns false if no channel is set (clarify not in progress).
func (sm *SessionManager) GetClarifyChannel(chatID int64) (chan string, bool) {
	v, ok := sm.clarifyChannels.Load(chatID)
	if !ok {
		return nil, false
	}
	return v.(chan string), true
}

// DeleteClarifyChannel removes the clarify channel for a chat (called after
// clarify completes or times out).
func (sm *SessionManager) DeleteClarifyChannel(chatID int64) {
	sm.clarifyChannels.Delete(chatID)
}

// SessionInfo is a lightweight summary of a session for listing.
type SessionInfo struct {
	ID        string    // session ID (e.g. "tg-12345")
	Task      string    // first user message or label
	CreatedAt time.Time // when the session started
	UpdatedAt time.Time // last activity
	Turns     int       // number of user turns
}

// ListSessions returns metadata for all sessions in the backing store,
// sorted by most-recent-first, limited to `limit` entries. If limit <= 0,
// all sessions are returned.
func (sm *SessionManager) ListSessions(limit int) ([]SessionInfo, error) {
	sessions, err := sm.Store.List(limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	infos := make([]SessionInfo, len(sessions))
	for i, s := range sessions {
		infos[i] = SessionInfo{
			ID:        s.ID,
			Task:      s.Task,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			Turns:     s.Turns,
		}
	}
	return infos, nil
}

// ResumeSession loads a session from the backing store and binds it to
// the given chatID. This replaces any existing session for that chat.
// sessionID can be a partial prefix match — the first matching session
// (by ID prefix or task contains) is used. Returns the new ChatSession
// or an error if no matching session is found.
func (sm *SessionManager) ResumeSession(chatID int64, sessionID string) (*ChatSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID required — use /sessions to list")
	}

	// Try direct load first.
	sess, err := sm.Store.Load(sessionID)
	if err != nil || sess == nil {
		// Prefix match: search all sessions for a match.
		all, listErr := sm.Store.List(0)
		if listErr != nil {
			return nil, fmt.Errorf("list sessions: %w", listErr)
		}
		for _, s := range all {
			if strings.HasPrefix(s.ID, sessionID) ||
				strings.Contains(strings.ToLower(s.Task), strings.ToLower(sessionID)) {
				sess = &s
				break
			}
		}
	}

	if sess == nil {
		return nil, fmt.Errorf("no session found matching %q", sessionID)
	}

	// Build ChatSession and cache it.
	cs := &ChatSession{
		ChatID:     chatID,
		SessionID:  sess.ID,
		Messages:   sess.Messages,
		CreatedAt:  sess.CreatedAt,
		LastActive: time.Now(),
		TurnCount:  sess.Turns,
	}

	sm.Mu.Lock()
	sm.Cache[chatID] = cs
	sm.Mu.Unlock()

	return cs, nil
}

// PruneSessions deletes sessions that haven't been updated in `days` days
// or more. Returns the number of sessions removed.
func (sm *SessionManager) PruneSessions(days int) (int, error) {
	if days <= 0 {
		days = 30
	}
	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return sm.Store.Cleanup(before)
}

// PrunePlans deletes plan files (~/.odek/plans/*.md) older than `days` days.
// Plans don't have a formal store yet — this scans the directory and checks
// file modification times. Returns the number of plan files removed. If the
// plans directory doesn't exist, returns 0, nil.
func (sm *SessionManager) PrunePlans(days int) (int, error) {
	if days <= 0 {
		days = 30
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, nil
	}
	plansDir := filepath.Join(home, ".odek", "plans")
	entries, err := os.ReadDir(plansDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("prune plans: read dir: %w", err)
	}

	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var removed int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(before) {
			path := filepath.Join(plansDir, e.Name())
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return removed, fmt.Errorf("prune plans: remove %q: %w", e.Name(), err)
			}
			removed++
		}
	}
	return removed, nil
}
