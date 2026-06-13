// Package session persists agent conversation history across runs.
//
// Sessions enable multi-turn conversations: a user runs a task, the agent
// responds, and the user continues the conversation with "odek continue",
// picking up the full message history from the previous turn.
//
// Storage: ~/.odek/sessions/<id>.json. Each file is a full conversation
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
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/redact"
)

// MaxSessionFileBytes caps the on-disk size of a session file that Load will
// read into memory. This prevents a tampered or corrupted multi-gigabyte
// session file from causing an OOM when any caller loads it.
const MaxSessionFileBytes = 32 * 1024 * 1024 // 32 MiB

// ── Types ──────────────────────────────────────────────────────────────

// Session represents a single multi-turn conversation with the agent.
// All fields are exported for direct manipulation at the CLI layer.
type Session struct {
	ID        string        `json:"id"`               // e.g. "20260518-abc123"
	CreatedAt time.Time     `json:"created_at"`       // first message time
	UpdatedAt time.Time     `json:"updated_at"`       // last append time
	Model     string        `json:"model"`            // model name used
	Turns     int           `json:"turns"`            // number of user turns
	Task      string        `json:"task"`             // first user message (label)
	Sandbox   bool          `json:"sandbox"`          // was sandboxed — auto-apply on resume
	Messages  []llm.Message `json:"messages"`         // full conversation history
	Buffer    []string      `json:"buffer,omitempty"` // last N turn summaries (memory tier 2)
}

// ── Store ──────────────────────────────────────────────────────────────

// Store manages session files in a directory on disk.
// Operations are simple file reads/writes — no locking, no caching.
type Store struct {
	dir string // e.g. /home/user/.odek/sessions/
	mu  sync.Mutex

	// Vec is the optional semantic search index. When non-nil, every
	// Save/Delete/Cleanup call updates the vector index automatically.
	// Call InitVectorIndex() to initialize.
	Vec *VectorIndex
}

// NewStore creates a session store rooted at ~/.odek/sessions/.
// The directory is created if it doesn't exist.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: home dir: %w", err)
	}
	dir := filepath.Join(home, ".odek", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("session: create dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// InitVectorIndex initializes the semantic search index using the embedding
// backend selected by cfg (nil = default RandomProjections). Must be called
// after NewStore, before the first Save. Safe to call multiple times —
// subsequent calls are no-ops once the index is ready.
func (s *Store) InitVectorIndex(cfg *embedding.Config) error {
	if s.Vec != nil && s.Vec.Ready() {
		return nil // already initialized
	}
	s.Vec = new(VectorIndex)
	return s.Vec.InitWithConfig(s.dir, cfg)
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

// ValidateSessionID validates that a session ID is safe for filesystem use.
// Rejects empty strings, path separators, traversal patterns, and dot names.
func ValidateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session: invalid ID %q: empty", id)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("session: invalid ID %q: reserved name", id)
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return fmt.Errorf("session: invalid ID %q: path separators not allowed", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("session: invalid ID %q: traversal not allowed", id)
	}
	if strings.Contains(id, "\x00") {
		return fmt.Errorf("session: invalid ID %q: null byte not allowed", id)
	}
	return nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Path returns the absolute filesystem path for a session file.
// Exported for testing and debugging.
func (s *Store) Path(id string) string { return s.path(id) }

// Dir returns the session store directory path.
// Exported for testing and debugging.
func (s *Store) Dir() string { return s.dir }

// idFromPath extracts the session ID from a filename like "20260518-abc123.json".
func idFromPath(name string) string {
	return strings.TrimSuffix(name, ".json")
}

// ── Index ──────────────────────────────────────────────────────────────

const indexFile = "index.json"

// IndexEntry holds minimal session metadata for the session index.
// This avoids loading every session file just to list or find the latest.
type IndexEntry struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     int       `json:"turns"`
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, indexFile)
}

// loadIndex reads the session index from disk.
// Returns an empty map if the index doesn't exist or can't be parsed
// (backward compat with existing session directories that have no index).
func (s *Store) loadIndex() map[string]*IndexEntry {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return make(map[string]*IndexEntry)
	}
	var entries []*IndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return make(map[string]*IndexEntry)
	}
	m := make(map[string]*IndexEntry, len(entries))
	for _, e := range entries {
		m[e.ID] = e
	}
	return m
}

// saveIndexLocked atomically writes the index to disk.
// Caller must hold s.mu.
func (s *Store) saveIndexLocked(idx map[string]*IndexEntry) error {
	entries := make([]*IndexEntry, 0, len(idx))
	for _, e := range idx {
		entries = append(entries, e)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("session: marshal index: %w", err)
	}
	if err := fsatomic.WriteFile(s.indexPath(), data, 0600); err != nil {
		return fmt.Errorf("session: write index: %w", err)
	}
	return nil
}

// indexEntry builds an IndexEntry from a Session.
func indexEntry(sess *Session) *IndexEntry {
	return &IndexEntry{
		ID:        sess.ID,
		Title:     sess.Task,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Turns:     sess.Turns,
	}
}

// isSessionFile returns true if the filename is a session JSON file
// (not the index file, not a directory, not a temp file).
func isSessionFile(name string) bool {
	return strings.HasSuffix(name, ".json") && name != indexFile && !strings.HasSuffix(name, ".tmp")
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
// and turn counts, and saves the result atomically.
// The full read-modify-write is serialized by s.mu to prevent both
// concurrent-write data loss and symlink-swap TOCTOU attacks.
func (s *Store) Append(id string, newMsgs []llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.Load(id)
	if err != nil {
		return err
	}
	sess.Messages = append(sess.Messages, newMsgs...)
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = countUserTurns(sess.Messages)
	return s.saveLocked(sess)
}

// Save writes a session to disk atomically and durably via fsatomic.WriteFile
// (temp-file → fsync → rename → dir fsync). This prevents:
//   - Partial writes from crashes (rename is atomic on POSIX)
//   - Data loss on power failure (the fsync flushes bytes before the rename)
//   - Symlink-following TOCTOU attacks (os.Rename replaces the
//     directory entry itself — it does NOT follow symlinks)
func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(sess)
}

// saveLocked is the internal write path — caller must hold s.mu.
// Writes to a temp file in the same directory, then atomically
// renames over the target. os.Rename replaces the directory entry
// without following symlinks, so a symlink swapped in between
// read and write gets replaced with a regular file.
// Also atomically updates the session index with the session's metadata.
func (s *Store) saveLocked(sess *Session) error {
	// Redact secrets from all messages before writing to disk.
	// This is defense-in-depth: the loop engine already redacts tool
	// outputs, but this catches any secrets that slipped through
	// (e.g. LLM hallucinations, direct API usage).
	for i := range sess.Messages {
		sess.Messages[i].Content = redact.RedactSecrets(sess.Messages[i].Content)
		sess.Messages[i].ReasoningContent = redact.RedactSecrets(sess.Messages[i].ReasoningContent)
	}

	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}

	if err := fsatomic.WriteFile(s.path(sess.ID), data, 0600); err != nil {
		return fmt.Errorf("session: write: %w", err)
	}

	// Update the index atomically.
	idx := s.loadIndex()
	idx[sess.ID] = indexEntry(sess)
	if err := s.saveIndexLocked(idx); err != nil {
		return err
	}

	// Update the vector index for semantic search.
	if s.Vec != nil {
		if err := s.Vec.Add(sess.ID, sess.Messages); err != nil {
			return fmt.Errorf("session: vector index add: %w", err)
		}
	}

	return nil
}

// Load reads a session from disk by ID. Returns an error if the file
// doesn't exist or can't be parsed.
func (s *Store) Load(id string) (*Session, error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	info, err := os.Stat(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("session: load %q: %w", id, err)
	}
	if info.Size() > MaxSessionFileBytes {
		return nil, fmt.Errorf("session: load %q: file too large (%d bytes, max %d)", id, info.Size(), MaxSessionFileBytes)
	}
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
// sessions exist. Returns an error when no sessions exist.
// Uses the session index for O(1) lookups. Falls back to scanning
// individual session files when no index exists (backward compat).
func (s *Store) Latest() (*Session, error) {
	idx := s.loadIndex()
	if len(idx) > 0 {
		var latestID string
		var latestTime time.Time
		for id, e := range idx {
			if latestID == "" || e.UpdatedAt.After(latestTime) {
				latestID = id
				latestTime = e.UpdatedAt
			}
		}
		return s.Load(latestID)
	}

	// Fallback: no index — scan directory.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}

	var latest *Session
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
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
// Uses the session index for O(n) reads (n = session count, but no
// JSON parsing per session). Falls back to loading each session file
// when no index exists (backward compat).
func (s *Store) List(limit int) ([]Session, error) {
	idx := s.loadIndex()
	if len(idx) > 0 {
		entries := make([]*IndexEntry, 0, len(idx))
		for _, e := range idx {
			entries = append(entries, e)
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		})

		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}

		sessions := make([]Session, len(entries))
		for i, e := range entries {
			sessions[i] = Session{
				ID:        e.ID,
				CreatedAt: e.CreatedAt,
				UpdatedAt: e.UpdatedAt,
				Task:      e.Title,
				Turns:     e.Turns,
				Messages:  nil,
			}
		}
		return sessions, nil
	}

	// Fallback: no index — scan directory.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
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

// Delete removes a session file from disk and removes its entry from
// the session index. Returns nil if the file doesn't exist (idempotent delete).
func (s *Store) Delete(id string) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// Remove from index atomically.
	idx := s.loadIndex()
	delete(idx, id)
	if err := s.saveIndexLocked(idx); err != nil {
		return err
	}

	// Remove from vector index.
	if s.Vec != nil {
		_ = s.Vec.Remove(id) // best-effort
	}

	return nil
}

// Cleanup deletes all sessions whose UpdatedAt is before the given time.
// Returns the count of deleted sessions. Idempotent — nonexistent files
// are skipped silently.
// Uses the session index for efficient batch operations. Falls back to
// scanning individual session files when no index exists (backward compat).
func (s *Store) Cleanup(before time.Time) (int, error) {
	idx := s.loadIndex()
	if len(idx) > 0 {
		s.mu.Lock()
		defer s.mu.Unlock()

		var deleted int
		for id, e := range idx {
			if e.UpdatedAt.Before(before) {
				if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
					return deleted, fmt.Errorf("session: delete %q: %w", id, err)
				}
				delete(idx, id)
				// Remove from vector index to prevent stale entries.
				if s.Vec != nil {
					_ = s.Vec.Remove(id) // best-effort
				}
				deleted++
			}
		}
		if deleted > 0 {
			if err := s.saveIndexLocked(idx); err != nil {
				return deleted, err
			}
		}
		return deleted, nil
	}

	// Fallback: no index — scan directory.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("session: list: %w", err)
	}

	var deleted int
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
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
// This excludes the system message (which is always first in odek sessions).
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
