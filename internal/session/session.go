// Package session persists agent conversation history across runs.
//
// Sessions enable multi-turn conversations: a user runs a task, the agent
// responds, and the user continues the conversation with "kode continue",
// picking up the full message history from the previous turn.
//
// Storage: ~/.kode/sessions/<id>.json. Each file is a full conversation
// transcript including system messages, user turns, assistant responses,
// tool calls, and tool results. Sessions are loaded by ID for continuation
// or by listing metadata for browsing.
//
// The Store is intentionally minimal — it's a JSON file manager, not a
// database. Session struct fields are all public, so callers can mutate
// the session directly and call Save(). This makes advanced operations
// (editing, truncating, merging sessions) trivial at the CLI layer.
package session

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
)

// ── Types ──────────────────────────────────────────────────────────────

// Session represents a single multi-turn conversation with the agent.
// All fields are exported for direct manipulation at the CLI layer.
type Session struct {
	ID        string         `json:"id"`         // e.g. "20260518-abc123"
	CreatedAt time.Time      `json:"created_at"` // first message time
	UpdatedAt time.Time      `json:"updated_at"` // last append time
	Model     string         `json:"model"`      // model name used
	Turns     int            `json:"turns"`       // number of user turns
	Task      string         `json:"task"`        // first user message (label)
	Sandbox   bool           `json:"sandbox"`     // was sandboxed — auto-apply on resume
	Messages  []llm.Message  `json:"messages"`    // full conversation history
}

// ── Store ──────────────────────────────────────────────────────────────

// Store manages session files in a directory on disk.
// Operations are simple file reads/writes — no locking, no caching.
type Store struct {
	dir string // e.g. /home/user/.kode/sessions/
}

// NewStore creates a session store rooted at ~/.kode/sessions/.
// The directory is created if it doesn't exist.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: home dir: %w", err)
	}
	dir := filepath.Join(home, ".kode", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("session: create dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// ── ID Generation ──────────────────────────────────────────────────────

// generateID creates a session ID: YYYYMMDD-<random 3 bytes hex>.
// The date prefix enables chronological sorting by filename.
// The random suffix avoids collisions from parallel runs.
func generateID() string {
	now := time.Now().UTC().Format("20060102")
	buf := make([]byte, 3)
	rand.Read(buf) //nolint:errcheck // always succeeds per docs
	return now + "-" + hexEncode(buf)
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

// ── Path helpers ───────────────────────────────────────────────────────

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// idFromPath extracts the session ID from a filename like "20260518-abc123.json".
func idFromPath(name string) string {
	return strings.TrimSuffix(name, ".json")
}

// ── CRUD ───────────────────────────────────────────────────────────────

// Create persists a new session with the given messages and metadata.
// It generates an ID, sets timestamps, counts user turns, and saves.
func (s *Store) Create(messages []llm.Message, model, task string) (*Session, error) {
	sess := &Session{
		ID:        generateID(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Model:     model,
		Turns:     countUserTurns(messages),
		Task:      task,
		Messages:  messages,
	}
	if err := s.Save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Append adds new messages to an existing session, updates timestamps
// and turn counts, and saves the result.
func (s *Store) Append(id string, newMsgs []llm.Message) error {
	sess, err := s.Load(id)
	if err != nil {
		return err
	}
	sess.Messages = append(sess.Messages, newMsgs...)
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = countUserTurns(sess.Messages)
	return s.Save(sess)
}

// Save writes a session to disk, overwriting any existing file with
// the same ID. This is the single write path — all mutations (append,
// edit, truncate, rename) go through Save().
func (s *Store) Save(sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	if err := os.WriteFile(s.path(sess.ID), data, 0600); err != nil {
		return fmt.Errorf("session: write: %w", err)
	}
	return nil
}

// Load reads a session from disk by ID. Returns an error if the file
// doesn't exist or can't be parsed.
func (s *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("session: load %q: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session: parse %q: %w", id, err)
	}
	return &sess, nil
}

// Latest returns the most recently updated session, or nil if no
// sessions exist. Returns an error when the store directory is empty.
func (s *Store) Latest() (*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}

	var latest *Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.Load(idFromPath(e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		if latest == nil || sess.UpdatedAt.After(latest.UpdatedAt) {
			latest = sess
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no sessions found")
	}
	return latest, nil
}

// List returns session summaries ordered by UpdatedAt descending
// (most recent first). limit caps the number returned (0 = all).
// Only metadata fields are populated — Messages is nil to keep
// listings lightweight.
func (s *Store) List(limit int) ([]Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.Load(idFromPath(e.Name()))
		if err != nil {
			continue
		}
		sess.Messages = nil // don't include full transcript in listings
		sessions = append(sessions, *sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

// Delete removes a session file from disk. Returns nil if the file
// doesn't exist (idempotent delete).
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Cleanup deletes all sessions whose UpdatedAt is before the given time.
// Returns the count of deleted sessions. Idempotent — nonexistent files
// are skipped silently (os.Remove already handles this via Delete).
func (s *Store) Cleanup(before time.Time) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("session: list: %w", err)
	}

	var deleted int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.Load(idFromPath(e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		if sess.UpdatedAt.Before(before) {
			if err := s.Delete(sess.ID); err != nil {
				return deleted, fmt.Errorf("session: delete %q: %w", sess.ID, err)
			}
			deleted++
		}
	}
	return deleted, nil
}

// ── Helpers ────────────────────────────────────────────────────────────

// countUserTurns returns the number of user messages in a slice.
// This excludes the system message (which is always first in kode sessions).
func countUserTurns(messages []llm.Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == "user" {
			count++
		}
	}
	return count
}

// GetMessages returns the session's message slice. Nil-safe.
// Returns an empty (non-nil) slice for a session with no messages.
func (s *Session) GetMessages() []llm.Message {
	if s == nil || s.Messages == nil {
		return []llm.Message{}
	}
	return s.Messages
}
