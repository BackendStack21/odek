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
	"github.com/BackendStack21/odek/internal/artifact"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/redact"
)

// MaxSessionFileBytes caps the on-disk size of a session file that Load will
// read into memory. This prevents a tampered or corrupted multi-gigabyte
// session file from causing an OOM when any caller loads it.
// It is a var, not a const, so tests can temporarily shrink it instead of
// building multi-MiB fixtures — production code should treat it as fixed.
var MaxSessionFileBytes = 32 * 1024 * 1024 // 32 MiB

// ── Types ──────────────────────────────────────────────────────────────

// Session represents a single multi-turn conversation with the agent.
// All fields are exported for direct manipulation at the CLI layer.
type Session struct {
	ID        string        `json:"id"`                   // e.g. "20260518-abc123…" (128-bit random suffix)
	AuthToken string        `json:"auth_token,omitempty"` // session-scoped secret required by serve handlers
	CreatedAt time.Time     `json:"created_at"`           // first message time
	UpdatedAt time.Time     `json:"updated_at"`           // last append time
	Model     string        `json:"model"`                // model name used
	Turns     int           `json:"turns"`                // number of user turns
	Task      string        `json:"task"`                 // first user message (label)
	Sandbox   bool          `json:"sandbox"`              // was sandboxed — auto-apply on resume
	Messages  []llm.Message `json:"messages"`             // full conversation history
	Buffer    []string      `json:"buffer,omitempty"`     // last N turn summaries (memory tier 2)

	// Pinned marks an operator-favorited session. Serve lists pinned
	// sessions first; it is pure presentation metadata.
	Pinned bool `json:"pinned,omitempty"`

	// Cumulative token usage across all turns of this session (provider
	// totals summed at each turn's completion). Presentation/observability
	// only — never used for budget enforcement (that is per-run, in
	// internal/budget).
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`

	// RedactBoundary records how many leading messages have already been
	// secret-redacted by a previous save. Redacting the full transcript on
	// every save is O(history) per write — O(n²) over a session's life —
	// and dominates save time on long sessions (20+ regexes over tens of
	// MB). Sessions are append-only, so only messages at or beyond the
	// boundary need scanning. Old files default to 0 (= redact all once).
	RedactBoundary int `json:"redact_boundary,omitempty"`

	// RedactBoundaryFP anchors RedactBoundary to the content it covered: a
	// short hash of the last message inside the boundary. An index alone is
	// unsound when the head of the history changes between saves (mid-run
	// context trimming drops front groups and the conversation later
	// re-grows past the stale boundary); a mismatch invalidates the
	// boundary and the next save re-redacts everything. Old files default
	// to "" (= treat any nonzero boundary as stale once, then re-anchor).
	RedactBoundaryFP string `json:"redact_boundary_fp,omitempty"`

	// ExternalRefs carries operator-supplied pointers to state that lives
	// outside odek (CI runs, dashboards, object stores — schema
	// odek-extension/v1, see docs/EXTENSIONS.md). odek stores and returns
	// these refs verbatim; it NEVER resolves or dereferences their URIs.
	ExternalRefs []ExternalRef `json:"external_refs,omitempty"`
}

// ExternalRef is an operator-supplied pointer to state that lives outside
// odek (a CI run, a dashboard, an object-store entry, …). odek stores and
// transports refs verbatim — it NEVER resolves or dereferences the URI.
// Schema: odek-extension/v1 (docs/EXTENSIONS.md).
type ExternalRef struct {
	Kind      string    `json:"kind"`                 // 1-64 chars, [a-z0-9_-]
	URI       string    `json:"uri"`                  // 1-2048 chars, no control characters
	CreatedBy string    `json:"created_by"`           // 1-128 chars
	ReadOnly  bool      `json:"read_only,omitempty"`  // hint for consumers; not enforced by odek
	CreatedAt time.Time `json:"created_at,omitempty"` // zero = stamped on first AddExternalRefs
}

// Validate checks the ref against the odek-extension/v1 constraints:
// kind 1-64 chars of [a-z0-9_-], uri 1-2048 chars without control
// characters, created_by 1-128 chars.
func (r ExternalRef) Validate() error {
	if len(r.Kind) == 0 || len(r.Kind) > 64 {
		return fmt.Errorf("session: external ref kind must be 1-64 chars, got %d", len(r.Kind))
	}
	for _, c := range r.Kind {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return fmt.Errorf("session: external ref kind %q: only lowercase ASCII letters, digits, '_' and '-' allowed", r.Kind)
		}
	}
	if len(r.URI) == 0 || len(r.URI) > 2048 {
		return fmt.Errorf("session: external ref uri must be 1-2048 chars, got %d", len(r.URI))
	}
	for _, c := range r.URI {
		if unicode.IsControl(c) {
			return fmt.Errorf("session: external ref uri contains a control character (U+%04X)", c)
		}
	}
	if len(r.CreatedBy) == 0 || len(r.CreatedBy) > 128 {
		return fmt.Errorf("session: external ref created_by must be 1-128 chars, got %d", len(r.CreatedBy))
	}
	return nil
}

// AddExternalRefs validates and appends refs to the session, skipping any
// that duplicate an existing ref on (kind, uri, created_by). It stamps
// CreatedAt on refs that leave it zero, and returns the number of refs
// actually added. The first invalid ref aborts with an error; refs already
// added stay added.
func (s *Session) AddExternalRefs(refs ...ExternalRef) (int, error) {
	added := 0
	for _, r := range refs {
		if err := r.Validate(); err != nil {
			return added, err
		}
		duplicate := false
		for _, e := range s.ExternalRefs {
			if e.Kind == r.Kind && e.URI == r.URI && e.CreatedBy == r.CreatedBy {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}
		s.ExternalRefs = append(s.ExternalRefs, r)
		added++
	}
	return added, nil
}

// ── Store ──────────────────────────────────────────────────────────────

// Store manages session files in a directory on disk.
// Operations are simple file reads/writes — no locking, no caching.
type Store struct {
	dir string // e.g. /home/user/.odek/sessions/
	mu  sync.Mutex

	// trimWarned records session IDs for which the write-path size-cap trim
	// warning has already been emitted, so the warning fires once per session
	// per process instead of on every Append of an oversized session.
	trimWarned map[string]struct{}

	// Vec is the optional semantic search index. When non-nil, every
	// Save/Delete/Cleanup call updates the vector index automatically
	// (SaveNoIndex is the deliberate per-turn exception). Call
	// InitVectorIndex() to initialize.
	Vec *VectorIndex

	// OnDelete, when non-nil, fires after a successful Delete or Cleanup
	// removal of a validated session id — OUTSIDE the store mutex, errors
	// swallowed (best-effort, like Vec.Remove). The delegate_tasks artifact
	// cascade uses it to remove the session's artifact subtree; a default is
	// wired in NewStoreWithDir and explicit assignments override it.
	OnDelete func(id string)
}

// NewStore creates a session store rooted at ~/.odek/sessions/.
// The directory is created if it doesn't exist.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: home dir: %w", err)
	}
	return NewStoreWithDir(filepath.Join(home, ".odek", "sessions"))
}

// NewStoreWithDir creates a session store rooted at the given directory.
// The directory is created if it doesn't exist. Used by subsystems (e.g.
// storage maintenance) that operate on an explicit home directory rather
// than the current user's default.
func NewStoreWithDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("session: create dir: %w", err)
	}
	s := &Store{dir: dir}
	// Default artifact cascade (SUBAGENT_RESULT_ARTIFACTS_PLAN.md §3.9):
	// delegate_tasks artifacts live in the sibling artifacts/ dir keyed by
	// session id; when a session is deleted its subtree dies with it, on
	// every deletion path (CLI, serve API, telegram, janitor Cleanup).
	// Best-effort; explicit OnDelete assignments override this default.
	artDir := filepath.Join(filepath.Dir(dir), "artifacts")
	s.OnDelete = func(id string) {
		_ = artifact.RemoveSessionSubtree(artDir, id)
	}
	return s, nil
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

// generateID creates a session ID: YYYYMMDD-<random 16 bytes hex>.
// The date prefix enables chronological sorting by filename.
// The 128-bit random suffix (32 hex chars) makes session IDs unguessable,
// preventing brute-force enumeration of transcript files.
func generateID() string {
	now := time.Now().UTC().Format("20060102")
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read only fails on catastrophic system failure. Fail
		// closed rather than minting a predictable timestamp-derived ID, which
		// would reintroduce the brute-force enumeration this randomness exists
		// to prevent.
		panic(fmt.Sprintf("session: crypto/rand unavailable: %v", err))
	}
	return now + "-" + hexEncode(buf)
}

// GenerateID returns a fresh, cryptographically random session ID. It is
// exported for callers (e.g. the CLI) that need to tag memory/context before
// a session is persisted.
func GenerateID() string { return generateID() }

// GenerateAuthToken creates a 256-bit URL-safe secret for session-scoped
// authentication in the Web UI. It is generated once when a session is created
// and required by serve handlers for any access to session details.
func GenerateAuthToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read only fails on catastrophic system failure. Fail
		// closed rather than minting a predictable timestamp-derived token,
		// which would be trivially guessable and defeat session auth.
		panic(fmt.Sprintf("session: crypto/rand unavailable: %v", err))
	}
	return hexEncode(buf)
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

	// Presentation metadata surfaced by listings (serve /api/sessions,
	// bodek). Older index files simply lack them — zero values.
	Model        string `json:"model,omitempty"`
	Pinned       bool   `json:"pinned,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
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
		ID:           sess.ID,
		Title:        sess.Task,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
		Turns:        sess.Turns,
		Model:        sess.Model,
		Pinned:       sess.Pinned,
		InputTokens:  sess.InputTokens,
		OutputTokens: sess.OutputTokens,
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
		AuthToken: GenerateAuthToken(),
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
	sess, err := s.Load(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	sess.Messages = append(sess.Messages, newMsgs...)
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = countUserTurns(sess.Messages)
	err = s.saveLocked(sess)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.addToVectorIndex(sess)
}

// Save writes a session to disk atomically and durably via fsatomic.WriteFile
// (temp-file → fsync → rename → dir fsync). This prevents:
//   - Partial writes from crashes (rename is atomic on POSIX)
//   - Data loss on power failure (the fsync flushes bytes before the rename)
//   - Symlink-following TOCTOU attacks (os.Rename replaces the
//     directory entry itself — it does NOT follow symlinks)
func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	err := s.saveLocked(sess)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.addToVectorIndex(sess)
}

// SaveNoIndex persists a session exactly like Save — redaction, file-cap
// trimming, atomic write, and index.json metadata update all still happen —
// but skips the vector-index update. It also refreshes UpdatedAt and Turns
// like Append does, so per-turn saves keep session metadata current.
// Used by the loop's per-turn persistence callback: embedding can be a
// remote HTTP call and must not fire on every loop iteration; the final
// end-of-run Save still indexes the completed session.
func (s *Store) SaveNoIndex(sess *Session) error {
	s.mu.Lock()
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = countUserTurns(sess.Messages)
	err := s.saveLocked(sess)
	s.mu.Unlock()
	return err
}

// addToVectorIndex updates the semantic search index for a session that is
// already persisted. It runs AFTER the store mutex is released: embedding can
// be a remote HTTP call (seconds), and holding s.mu across it would serialize
// every concurrent Load/List/Save behind network latency. The vector index
// has its own locking, and the session file is already on disk, so a
// not-ready index that rebuilds from disk picks this session up.
func (s *Store) addToVectorIndex(sess *Session) error {
	if s.Vec == nil {
		return nil
	}
	if err := s.Vec.Add(sess.ID, sess.Messages); err != nil {
		return fmt.Errorf("session: vector index add: %w", err)
	}
	return nil
}

// saveLocked is the internal write path — caller must hold s.mu.
// Writes to a temp file in the same directory, then atomically
// renames over the target. os.Rename replaces the directory entry
// without following symlinks, so a symlink swapped in between
// read and write gets replaced with a regular file.
// Also atomically updates the session index with the session's metadata.
// redactMessageFP fingerprints a message for the RedactBoundary anchor:
// deterministic over the (already-redacted) persisted form, so an unchanged
// head matches across saves and any trim/rewrite invalidates the boundary.
func redactMessageFP(m llm.Message) string {
	h := sha256.Sum256([]byte(m.Role + "\x00" + m.Content + "\x00" + m.ReasoningContent))
	return hex.EncodeToString(h[:8])
}

func (s *Store) saveLocked(sess *Session) error {
	// Reject malformed or traversal-bearing session IDs before the ID is used
	// to build a filesystem path. A planted session file with an embedded
	// "id":"../config" must not cause a subsequent Save/Append to overwrite
	// files outside the session directory.
	if err := ValidateSessionID(sess.ID); err != nil {
		return fmt.Errorf("session: refusing unsafe save: %w", err)
	}

	// Redact secrets before writing to disk. This is defense-in-depth: the
	// loop engine already redacts tool outputs, but this catches any secrets
	// that slipped through (e.g. LLM hallucinations, direct API usage, or
	// the first user prompt stored as the session title). Sessions are
	// append-only, so only messages at or beyond sess.RedactBoundary are
	// scanned — messages already redacted by a previous save are not
	// re-scanned (see the field comment for the O(n²) rationale).
	sess.Task = redact.RedactSecrets(sess.Task)
	boundary := sess.RedactBoundary
	if boundary < 0 {
		boundary = 0
	}
	if boundary > len(sess.Messages) {
		boundary = len(sess.Messages)
	}
	// The boundary is an INDEX, so it is only sound while the head of the
	// history is unchanged. Mid-run context trimming drops front groups and
	// later turns re-grow past the stale boundary, leaving never-redacted
	// messages below it (2026-08 audit: tool *error* text is never redacted
	// in memory, so the save-time scan is the only layer covering it).
	// Anchor the boundary to a fingerprint of the last message it covered;
	// on any mismatch — or a legacy session with no fingerprint — redact
	// the whole transcript (idempotent for already-redacted text).
	if boundary > 0 {
		if sess.RedactBoundaryFP == "" || redactMessageFP(sess.Messages[boundary-1]) != sess.RedactBoundaryFP {
			boundary = 0
		}
	}
	for i := boundary; i < len(sess.Messages); i++ {
		sess.Messages[i].Content = redact.RedactSecrets(sess.Messages[i].Content)
		sess.Messages[i].ReasoningContent = redact.RedactSecrets(sess.Messages[i].ReasoningContent)
	}

	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}

	// Write-path size cap: MaxSessionFileBytes is enforced at Load, so a
	// session allowed to grow past it on disk would become unloadable. Trim
	// the oldest message groups (keeping the system message at index 0 and
	// the most recent turns, mirroring the loop's trim semantics) until the
	// serialized form fits.
	if len(data) > MaxSessionFileBytes {
		// The trim's own marshaled form is discarded: the final marshal
		// below recomputes it after RedactBoundary is updated.
		if _, err = s.trimToFileCapLocked(sess, data); err != nil {
			return err
		}
		if s.trimWarned == nil {
			s.trimWarned = make(map[string]struct{})
		}
		if _, ok := s.trimWarned[sess.ID]; !ok {
			s.trimWarned[sess.ID] = struct{}{}
			fmt.Fprintf(os.Stderr, "odek: warning: session %s exceeded %d bytes on write — oldest messages trimmed to stay within the load cap\n", sess.ID, MaxSessionFileBytes)
		}
	}
	// Every surviving message is now redacted: those before the boundary by
	// earlier saves, the rest just now — and trimming only removes messages,
	// so the boundary is simply the surviving count. Set it (and its
	// fingerprint anchor) before the final marshal so they persist.
	sess.RedactBoundary = len(sess.Messages)
	sess.RedactBoundaryFP = ""
	if n := len(sess.Messages); n > 0 {
		sess.RedactBoundaryFP = redactMessageFP(sess.Messages[n-1])
	}
	data, err = json.Marshal(sess)
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

	// Note: the vector index is updated by the caller (addToVectorIndex) after
	// s.mu is released — embedding may be a slow remote call and must not run
	// under the store mutex.

	return nil
}

// trimToFileCapLocked drops the oldest message groups from sess until its
// serialized form fits within MaxSessionFileBytes, returning the trimmed
// JSON. Caller must hold s.mu.
//
// Group semantics mirror the loop's context trimming: the system message at
// index 0 is always kept, and an assistant tool_calls message is dropped
// together with its following tool-result messages so a stored transcript
// never contains orphaned tool messages (which strict providers reject).
// The turn count is recounted to match the surviving messages. When any
// groups were dropped, a marker system message is inserted after the system
// prompt so a resumed session can see that earlier history was removed.
// If nothing droppable remains (a degenerate case, e.g. a single oversized
// system message), the session is written as-is — failing the save would
// lose data.
func (s *Store) trimToFileCapLocked(sess *Session, data []byte) ([]byte, error) {
	droppedGroups := 0
	for len(data) > MaxSessionFileBytes {
		start := 0
		if len(sess.Messages) > 0 && sess.Messages[0].Role == "system" {
			start = 1 // keep system
		}
		if start >= len(sess.Messages) {
			break // nothing left to drop
		}
		// Drop enough oldest groups in ONE pass to get back under the cap.
		// Dropping a single group per re-marshal is O(n²) for long
		// transcripts (thousands of messages × full-session marshal) and
		// effectively hangs on oversized fixtures. Each pass drops at least
		// one group, so the outer loop still terminates; groups are
		// marshaled once each to size them exactly.
		excess := len(data) - MaxSessionFileBytes
		freed := 0
		dropEnd := start
		for dropEnd < len(sess.Messages) && freed <= excess {
			groupEnd := dropEnd + 1
			if sess.Messages[dropEnd].Role == "assistant" && len(sess.Messages[dropEnd].ToolCalls) > 0 {
				for groupEnd < len(sess.Messages) && sess.Messages[groupEnd].Role == "tool" {
					groupEnd++
				}
			}
			groupJSON, err := json.Marshal(sess.Messages[dropEnd:groupEnd])
			if err != nil {
				return nil, fmt.Errorf("session: marshal trim candidate: %w", err)
			}
			freed += len(groupJSON)
			droppedGroups++
			dropEnd = groupEnd
		}
		sess.Messages = append(sess.Messages[:start], sess.Messages[dropEnd:]...)
		sess.Turns = countUserTurns(sess.Messages)
		var err error
		data, err = json.Marshal(sess)
		if err != nil {
			return nil, fmt.Errorf("session: marshal after trim: %w", err)
		}
	}

	// Persist a marker so a resumed session knows earlier turns were removed
	// (the stderr warning alone never reaches the transcript).
	if droppedGroups > 0 {
		marker := llm.Message{
			Role: "system",
			Content: fmt.Sprintf(
				"[Session storage limit: %d oldest message group(s) were removed from this transcript to stay within the %d-byte file cap. Earlier conversation context is unavailable.]",
				droppedGroups, MaxSessionFileBytes,
			),
		}
		insertAt := 0
		if len(sess.Messages) > 0 && sess.Messages[0].Role == "system" {
			insertAt = 1
		}
		withMarker := make([]llm.Message, 0, len(sess.Messages)+1)
		withMarker = append(withMarker, sess.Messages[:insertAt]...)
		withMarker = append(withMarker, marker)
		withMarker = append(withMarker, sess.Messages[insertAt:]...)

		candidate := *sess
		candidate.Messages = withMarker
		markerData, err := json.Marshal(&candidate)
		if err != nil {
			return nil, fmt.Errorf("session: marshal trim marker: %w", err)
		}
		// Keep the marker only if the transcript still fits the cap.
		if len(markerData) <= MaxSessionFileBytes {
			sess.Messages = withMarker
			data = markerData
		}
	}
	return data, nil
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
	if info.Size() > int64(MaxSessionFileBytes) {
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
	// The on-disk ID must match the filename it was loaded from. This prevents
	// a planted session file from redirecting a later Save/Append to a path
	// derived from an attacker-controlled embedded ID.
	if sess.ID != id {
		return nil, fmt.Errorf("session: load %q: ID mismatch (file contains %q)", id, sess.ID)
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
		// Walk candidates newest-first and return the first one whose file
		// still loads. A stale entry (file deleted before the index was
		// rewritten — Delete removes the file first, then updates the index)
		// must not break the lookup when valid sessions exist.
		ids := make([]string, 0, len(idx))
		for id := range idx {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return idx[ids[i]].UpdatedAt.After(idx[ids[j]].UpdatedAt)
		})
		for _, id := range ids {
			if err := ValidateSessionID(id); err != nil {
				continue // corrupt/planted entry — skip, never touch the fs
			}
			if _, err := os.Stat(s.path(id)); err != nil {
				continue // stale entry
			}
			// The file exists but may still fail to Load (over the size cap,
			// parse error, ID mismatch). trimToFileCapLocked deliberately
			// writes an over-cap session rather than losing it, so this is
			// reachable through normal operation. Skip to the next candidate
			// instead of failing the lookup — "the first one whose file still
			// loads".
			sess, err := s.Load(id)
			if err == nil {
				return sess, nil
			}
		}
		// Every indexed entry was stale — fall through to a directory scan.
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

		// Drop entries whose session file no longer exists (stale index) or
		// whose ID is unsafe for filesystem use (planted entry): listings
		// must not show phantom sessions, and the ID is echoed to callers.
		live := entries[:0]
		for _, e := range entries {
			if ValidateSessionID(e.ID) != nil {
				continue
			}
			if _, err := os.Stat(s.path(e.ID)); err != nil {
				continue
			}
			live = append(live, e)
		}
		entries = live

		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}

		sessions := make([]Session, len(entries))
		for i, e := range entries {
			sessions[i] = Session{
				ID:           e.ID,
				CreatedAt:    e.CreatedAt,
				UpdatedAt:    e.UpdatedAt,
				Task:         e.Title,
				Turns:        e.Turns,
				Model:        e.Model,
				Pinned:       e.Pinned,
				InputTokens:  e.InputTokens,
				OutputTokens: e.OutputTokens,
				Messages:     nil,
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
// Fires OnDelete (when set) after a successful removal.
func (s *Store) Delete(id string) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}

	s.mu.Lock()
	err := s.removeLocked(id)
	if err == nil {
		idx := s.loadIndex()
		delete(idx, id)
		err = s.saveIndexLocked(idx)
	}
	s.mu.Unlock()

	if err == nil && s.OnDelete != nil {
		s.OnDelete(id)
	}
	return err
}

// removeLocked deletes the session FILE and its vector-index entry. The
// store mutex must be held. A missing file is nil (idempotent).
func (s *Store) removeLocked(id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Remove from vector index to prevent stale entries.
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

		var deleted, purged int
		var cascaded []string
		for id, e := range idx {
			// Validate before filesystem use: a planted/tampered index entry
			// must not direct deletions outside the store dir (same threat
			// model as Load/saveLocked, which reject embedded IDs).
			if err := ValidateSessionID(id); err != nil {
				delete(idx, id) // corrupt entry — purge from index only
				purged++
				continue
			}
			if e.UpdatedAt.Before(before) {
				if err := s.removeLocked(id); err != nil {
					s.mu.Unlock()
					return deleted, fmt.Errorf("session: delete %q: %w", id, err)
				}
				delete(idx, id)
				cascaded = append(cascaded, id)
				deleted++
			}
		}
		if deleted > 0 || purged > 0 {
			if err := s.saveIndexLocked(idx); err != nil {
				s.mu.Unlock()
				return deleted, err
			}
		}
		s.mu.Unlock()
		if s.OnDelete != nil {
			for _, id := range cascaded {
				s.OnDelete(id)
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
