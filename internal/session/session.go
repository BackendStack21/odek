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
// database. Session content fields are public, so callers can mutate
// the session directly and call Save(). This makes advanced operations
// (editing, truncating, merging sessions) trivial at the CLI layer.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/redact"
)

// MaxSessionFileBytes caps the on-disk size of a session file that Load will
// read into memory. This prevents a tampered or corrupted multi-gigabyte
// session file from causing an OOM when any caller loads it.
// It is a var, not a const, so tests can temporarily shrink it instead of
// building multi-MiB fixtures — production code should treat it as fixed.
var MaxSessionFileBytes = 32 * 1024 * 1024 // 32 MiB

// ── Types ──────────────────────────────────────────────────────────────

// Decision records a principal-channel choice separately from model context.
// Receipt metadata is bounded and redacted when it crosses the store boundary.
type Decision struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Command string    `json:"command,omitempty"`
	Risk    string    `json:"risk,omitempty"`
	Action  string    `json:"action,omitempty"`
	State   string    `json:"state"`
	TurnID  string    `json:"turn_id,omitempty"`
	At      time.Time `json:"at"`
}

// Session represents a single multi-turn conversation with the agent.
// Content fields are exported for direct manipulation at the CLI layer.
type Session struct {
	// A loaded snapshot may update its original ID only while it still exists.
	// Assigning a new ID explicitly creates a separate session.
	persistedID string

	// Revision is checked under the store's cross-process write lock. A stale
	// loaded snapshot can never replace a newer committed transcript.
	Revision uint64 `json:"revision,omitempty"`
	// Generation distinguishes deletion/recreation of a fixed session ID.
	// Revisions alone cannot reject an old snapshot after a counter restarts.
	Generation string `json:"generation,omitempty"`

	ID        string     `json:"id"`                   // e.g. "20260518-abc123…" (128-bit random suffix)
	AuthToken string     `json:"auth_token,omitempty"` // session-scoped secret required by serve handlers
	CreatedAt time.Time  `json:"created_at"`           // first message time
	UpdatedAt time.Time  `json:"updated_at"`           // last append time
	Model     string     `json:"model"`                // model name used
	Provider  string     `json:"provider,omitempty"`   // LLM provider id used (v2; empty on pre-v2 files)
	Turns     int        `json:"turns"`                // number of user turns
	Task      string     `json:"task"`                 // first user message (label)
	Sandbox   bool       `json:"sandbox"`              // was sandboxed — auto-apply on resume
	Messages  []Message  `json:"messages"`             // full conversation history
	Decisions []Decision `json:"decisions,omitempty"`
	Buffer    []string   `json:"buffer,omitempty"` // last N turn summaries (memory tier 2)

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

// ErrConflict reports that another writer committed after this snapshot was
// loaded. The caller must reload and reconcile; retrying the stale save is not
// safe for an execution transcript.
var ErrConflict = errors.New("session: stale revision")

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
		// Persistence stores the redacted URI, so a re-added reference whose
		// URI carries a secret must be compared in redacted form on both
		// sides or every save-then-add cycle would append a duplicate.
		redactedURI := redact.RedactSecrets(r.URI)
		duplicate := false
		for _, e := range s.ExternalRefs {
			if e.Kind == r.Kind && e.CreatedBy == r.CreatedBy &&
				(e.URI == r.URI || redact.RedactSecrets(e.URI) == redactedURI) {
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

// redactExternalRefs returns a copy of refs with every URI passed through
// the secret redactor; the caller's slice is left untouched.
func redactExternalRefs(in []ExternalRef) []ExternalRef {
	refs := append([]ExternalRef(nil), in...)
	for i := range refs {
		refs[i].URI = redact.RedactSecrets(refs[i].URI)
	}
	return refs
}

// ── Store ──────────────────────────────────────────────────────────────

// Store manages session files in a directory on disk.
// Operations are simple file reads/writes — no locking, no caching.
type Store struct {
	dir string // e.g. /home/user/.odek/sessions/
	mu  sync.Mutex

	// marshalCount counts session marshals performed by saveLocked. It
	// exists to let tests assert the single-marshal-per-save contract;
	// reads/writes happen under mu.
	marshalCount int

	// promptDigests remembers, per session id, a keyed digest of the redacted
	// principal prompts written by the last save. Guarded by mu.
	promptDigests map[string]promptDigest

	// revStamps remembers, per session id, the (generation, revision) this
	// store last persisted together with the file stamp it produced, so the
	// next save can verify the on-disk file is untouched with an lstat instead
	// of a full parse. Guarded by mu.
	revStamps map[string]revStamp

	// listStats counts per-entry existence stats made by List. Test
	// observability only.
	listStats atomic.Int64

	// indexWrites counts index.json rewrites. Test observability only;
	// guarded by mu.
	indexWrites int

	// revisionLoads counts full session loads performed by saveLocked for the
	// revision check. Test observability only; guarded by mu.
	revisionLoads int

	// promptRedactions counts RedactSecrets calls made on principal prompts
	// by saveLocked. Test observability only; guarded by mu.
	promptRedactions int

	// indexDiskReads counts how many times loadIndex actually read
	// index.json from disk (cache misses). Test observability only;
	// guarded by idxMu.
	indexDiskReads int

	// idxMu guards the index cache below. It is a separate lock because
	// Latest/List read the index without holding mu; lock order when both
	// are held: mu → idxMu.
	idxMu sync.Mutex
	// idxCache is the parsed index keyed by session id, with idxMod/idxSize
	// stamping the on-disk file it was built from. Any observable change to
	// index.json (mtime or size) triggers a reload, so an index rewritten by
	// another odek process is picked up on the next load.
	idxCache  map[string]*IndexEntry
	idxLoaded bool
	idxMod    time.Time
	idxSize   int64
	idxIno    uint64

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

// loadIndex returns the session index, using the in-memory cache whenever
// index.json is unchanged on disk (mtime+size stamp). The returned map is a
// fresh copy: callers mutate it freely without corrupting the cache.
// Backward compatible with session directories that have no index (empty map).
func (s *Store) loadIndex() map[string]*IndexEntry {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.idxCache == nil {
		s.idxCache = make(map[string]*IndexEntry)
	}
	info, err := os.Stat(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			// File gone: reset so callers observe the absence instead of
			// stale cached entries.
			s.idxCache = make(map[string]*IndexEntry)
			s.idxLoaded = false
			s.idxMod, s.idxSize, s.idxIno = time.Time{}, 0, 0
			s.indexDiskReads++
			return make(map[string]*IndexEntry)
		}
		// Transient stat error (permissions, EINTR, network mount hiccup):
		// never wipe the cache on it — an empty map would make every
		// session vanish from listings. Serve the last known-good copy.
		s.indexDiskReads++
		return copyIndex(s.idxCache)
	}
	if info.ModTime().Equal(s.idxMod) && info.Size() == s.idxSize && fileInode(info) == s.idxIno && s.idxLoaded {
		return copyIndex(s.idxCache)
	}
	s.indexDiskReads++
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return make(map[string]*IndexEntry)
	}
	var entries []*IndexEntry
	m := make(map[string]*IndexEntry)
	if err := json.Unmarshal(data, &entries); err == nil {
		for _, e := range entries {
			m[e.ID] = e
		}
	}
	// Parse failures keep the previous stamp uncached so a later, valid
	// rewrite is always re-read.
	if err == nil {
		s.idxCache = m
		s.idxLoaded = true
		s.idxMod, s.idxSize = info.ModTime(), info.Size()
		s.idxIno = fileInode(info)
	}
	return copyIndex(m)
}

func copyIndex(m map[string]*IndexEntry) map[string]*IndexEntry {
	out := make(map[string]*IndexEntry, len(m))
	for k, v := range m {
		e := *v
		out[k] = &e
	}
	return out
}

// fileLock acquires an exclusive flock on sessions.lock so index.json
// read-modify-write is serialized across processes. Caller must hold s.mu
// first (lock order: mu → flock). A lock failure aborts the mutation
// rather than proceeding as last-writer-wins.
func (s *Store) fileLock() (func(), error) {
	rel, err := flock.Lock(filepath.Join(s.dir, "sessions.lock"))
	if err != nil {
		return nil, fmt.Errorf("session: lock: %w", err)
	}
	return rel, nil
}

// saveIndexLocked atomically writes the index to disk and refreshes the
// in-memory cache. Caller must hold s.mu. idx is owned by the caller; the
// store keeps its own copy.
func (s *Store) saveIndexLocked(idx map[string]*IndexEntry) error {
	s.indexWrites++
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
	if info, err := os.Stat(s.indexPath()); err == nil {
		s.idxMu.Lock()
		s.idxCache = copyIndex(idx)
		s.idxLoaded = true
		s.idxMod, s.idxSize = info.ModTime(), info.Size()
		s.idxIno = fileInode(info)
		s.idxMu.Unlock()
	}
	return nil
}

// indexLagWindow bounds how far a per-step checkpoint lets the indexed
// UpdatedAt trail the session file. The end-of-turn Save always rewrites the
// index, so listings are exact whenever a turn is not in flight.
const indexLagWindow = 2 * time.Second

// indexMayLag reports whether rewriting index.json for next can be skipped:
// only the volatile fields (UpdatedAt, token counters) moved, and UpdatedAt by
// less than indexLagWindow.
func indexMayLag(old, next IndexEntry) bool {
	d := next.UpdatedAt.Sub(old.UpdatedAt)
	return d >= 0 && d < indexLagWindow &&
		old.Title == next.Title &&
		old.Model == next.Model &&
		old.Pinned == next.Pinned &&
		old.Turns == next.Turns &&
		old.CreatedAt.Equal(next.CreatedAt)
}

// peekIndexEntry returns one entry of the current index without copying the
// whole map when the in-memory cache is fresh.
func (s *Store) peekIndexEntry(id string) (IndexEntry, bool) {
	s.idxMu.Lock()
	if s.idxLoaded {
		if info, err := os.Stat(s.indexPath()); err == nil &&
			info.ModTime().Equal(s.idxMod) && info.Size() == s.idxSize && fileInode(info) == s.idxIno {
			e, ok := s.idxCache[id]
			s.idxMu.Unlock()
			if !ok {
				return IndexEntry{}, false
			}
			return *e, true
		}
	}
	s.idxMu.Unlock()
	if e, ok := s.loadIndex()[id]; ok {
		return *e, true
	}
	return IndexEntry{}, false
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
func (s *Store) Create(messages []Message, model, task string) (*Session, error) {
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
func (s *Store) Append(id string, newMsgs []Message) error {
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
// trimming and atomic write all still happen, and index.json is refreshed
// unless the indexed summary would trail by under indexLagWindow (only
// UpdatedAt and token counters moved) — but it skips the vector-index update. It also refreshes UpdatedAt and Turns
// like Append does, so per-turn saves keep session metadata current.
// Used by the loop's per-turn persistence callback: embedding can be a
// remote HTTP call and must not fire on every loop iteration; the final
// end-of-run Save still indexes the completed session.
func (s *Store) SaveNoIndex(sess *Session) error {
	s.mu.Lock()
	sess.UpdatedAt = time.Now().UTC()
	sess.Turns = countUserTurns(sess.Messages)
	err := s.saveLockedMode(sess, true)
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
		diagnostics.Report("session", "vector_index", sess.ID, err)
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
func redactMessageFP(m Message) string {
	h := sha256.Sum256([]byte(m.Role + "\x00" + m.Content + "\x00" + m.ReasoningContent))
	return hex.EncodeToString(h[:8])
}

// maxRevStamps bounds the revision stamp memo.
const maxRevStamps = 1024

// revStampSettle is how old a stamp must be before it is trusted without a
// full Load. Kernel file clocks tick coarsely (ext4, tmpfs: up to ~10 ms), so
// two writes inside one tick can share size and mtime.
const revStampSettle = 50 * time.Millisecond

// revStamp is a persisted (generation, revision) pair plus the identity of the
// file it was written to.
type revStamp struct {
	generation string
	revision   uint64
	size       int64
	mod        time.Time
	ino        uint64
}

// cachedRevision returns the revision this store last persisted for id when
// the file on disk is provably the one it wrote (same inode, size and
// nanosecond mtime), else nil so the caller falls back to a full Load. A
// filesystem with whole-second mtimes can not distinguish two writes in one
// tick, so it never takes the fast path; neither does a platform without
// inode numbers, nor a stamp younger than revStampSettle, inside which a
// coarse kernel clock could still make a foreign rewrite look identical.
func (s *Store) cachedRevision(id string, info os.FileInfo, statErr error) *Session {
	if statErr != nil || info == nil || !info.Mode().IsRegular() {
		return nil
	}
	st, ok := s.revStamps[id]
	if !ok || st.mod.Nanosecond() == 0 || st.ino == 0 || time.Since(st.mod) < revStampSettle {
		return nil
	}
	if info.Size() != st.size || !info.ModTime().Equal(st.mod) || fileInode(info) != st.ino {
		return nil
	}
	return &Session{ID: id, Generation: st.generation, Revision: st.revision, persistedID: id}
}

// rememberRevision stamps the file just written for sess.
func (s *Store) rememberRevision(sess *Session) {
	info, err := os.Lstat(s.path(sess.ID))
	if err != nil || !info.Mode().IsRegular() {
		delete(s.revStamps, sess.ID)
		return
	}
	if s.revStamps == nil || len(s.revStamps) >= maxRevStamps {
		s.revStamps = make(map[string]revStamp)
	}
	s.revStamps[sess.ID] = revStamp{
		generation: sess.Generation,
		revision:   sess.Revision,
		size:       info.Size(),
		mod:        info.ModTime(),
		ino:        fileInode(info),
	}
}

// maxPromptDigests bounds the per-session prompt digest memo.
const maxPromptDigests = 256

type promptDigest struct {
	n    int
	hash uint64
	// gen is the redaction registry generation the prompts were scanned
	// under; a secret registered later invalidates the memo.
	gen uint64
}

var promptSeed = maphash.MakeSeed()

// promptsHash digests the principal prompts of msgs[:n] (presence, length and
// bytes) with a process-keyed hash.
func promptsHash(msgs []Message, n int) uint64 {
	var h maphash.Hash
	h.SetSeed(promptSeed)
	var lenBuf [8]byte
	for i := 0; i < n; i++ {
		p := msgs[i].PrincipalPrompt
		if p == nil {
			h.WriteByte(0)
			continue
		}
		h.WriteByte(1)
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(*p)))
		h.Write(lenBuf[:])
		h.WriteString(*p)
	}
	return h.Sum64()
}

// promptsUnchanged reports whether the first n prompts are byte-identical to
// what the previous save of this session persisted (already redacted).
func (s *Store) promptsUnchanged(id string, msgs []Message, n int) bool {
	d, ok := s.promptDigests[id]
	return ok && d.n == n && n <= len(msgs) && d.gen == redact.Generation() && d.hash == promptsHash(msgs, n)
}

// rememberPrompts records the digest of the redacted prompts just persisted,
// under the registry generation gen that was current before they were scanned.
func (s *Store) rememberPrompts(id string, msgs []Message, gen uint64) {
	if s.promptDigests == nil || len(s.promptDigests) >= maxPromptDigests {
		s.promptDigests = make(map[string]promptDigest)
	}
	s.promptDigests[id] = promptDigest{n: len(msgs), hash: promptsHash(msgs, len(msgs)), gen: gen}
}

// stripReturnAfterBreak drops return-after-break summaries before a save:
// the summary is run-only presentation, so persisting it would add a copy on
// every resume. The input slice is never modified.
func stripReturnAfterBreak(msgs []Message) []Message {
	n := 0
	for _, m := range msgs {
		if m.Name == ReturnAfterBreakName {
			n++
		}
	}
	if n == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs)-n)
	for _, m := range msgs {
		if m.Name != ReturnAfterBreakName {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) saveLocked(sess *Session) error {
	return s.saveLockedMode(sess, false)
}

// saveLockedMode is saveLocked; lazyIndex lets a per-step checkpoint leave
// index.json untouched when the indexed summary would only move forward by
// less than indexLagWindow (see indexMayLag).
func (s *Store) saveLockedMode(sess *Session, lazyIndex bool) (err error) {
	defer func() { diagnostics.Report("session", "save", sess.ID, err) }()
	// Reject malformed or traversal-bearing session IDs before the ID is used
	// to build a filesystem path. A planted session file with an embedded
	// "id":"../config" must not cause a subsequent Save/Append to overwrite
	// files outside the session directory.
	if err := ValidateSessionID(sess.ID); err != nil {
		return fmt.Errorf("session: refusing unsafe save: %w", err)
	}
	sess.Messages = stripReturnAfterBreak(sess.Messages)
	unlock, err := s.fileLock()
	if err != nil {
		return err
	}
	defer unlock()
	info, statErr := os.Lstat(s.path(sess.ID))
	alias := statErr == nil && info.Mode()&os.ModeSymlink != 0
	var current *Session
	var loadErr error
	if !alias {
		if c := s.cachedRevision(sess.ID, info, statErr); c != nil {
			current = c
		} else {
			s.revisionLoads++
			current, loadErr = s.Load(sess.ID)
		}
	}
	if alias {
		// Atomic replacement owns this directory entry, never the alias's
		// target. Do not read the target to check its unrelated revision.
	} else if loadErr == nil {
		if sess.persistedID != "" && sess.persistedID != sess.ID {
			return fmt.Errorf("%w: destination session already exists", ErrConflict)
		}
		if current.Generation != sess.Generation || current.Revision != sess.Revision {
			return fmt.Errorf("%w: have %d, current %d", ErrConflict, sess.Revision, current.Revision)
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	} else if sess.persistedID == sess.ID {
		return fmt.Errorf("session: cannot update removed session: %w", loadErr)
	}
	previousRevision := sess.Revision
	previousGeneration := sess.Generation
	if current == nil && sess.persistedID != sess.ID {
		sess.Revision = 0
		sess.Generation = ""
	}
	if sess.Generation == "" {
		sess.Generation = generateID()
	}
	if sess.Revision == ^uint64(0) {
		return fmt.Errorf("session: revision exhausted")
	}
	sess.Revision++
	committed := false
	// saveLocked mutates sess in place (secret redaction, capacity trim,
	// boundary advance). A failed save must leave the caller's snapshot
	// untouched: otherwise the memory copy stays trimmed while the on-disk
	// revision never advanced (or diverged), and the next Save can hit
	// ErrConflict forever with unsaved turns. Snapshot everything the
	// mutation touches and restore it on any error return.
	var (
		snapshotTask = sess.Task
		// Element copy, not a header copy: trimToFileCapLocked compacts the
		// slice IN PLACE, which would corrupt the shared backing array under
		// a header-copy snapshot and restore shifted/garbled contents after
		// a failed post-trim save.
		snapshotMessages   = append([]Message(nil), sess.Messages...)
		snapshotBoundary   = sess.RedactBoundary
		snapshotBoundaryFP = sess.RedactBoundaryFP
	)
	defer func() {
		if err != nil {
			sess.Task = snapshotTask
			sess.Messages = snapshotMessages
			sess.RedactBoundary = snapshotBoundary
			sess.RedactBoundaryFP = snapshotBoundaryFP
		}
		if !committed {
			sess.Revision = previousRevision
			sess.Generation = previousGeneration
		}
	}()

	// Redact secrets before writing to disk. This is defense-in-depth: the
	// loop engine already redacts tool outputs, but this catches any secrets
	// that slipped through (e.g. LLM hallucinations, direct API usage, or
	// the first user prompt stored as the session title). Sessions are
	// append-only, so only messages at or beyond sess.RedactBoundary are
	// scanned — messages already redacted by a previous save are not
	// re-scanned (see the field comment for the O(n²) rationale).
	sess.Task = redact.RedactSecrets(sess.Task)
	if len(sess.Decisions) > 128 {
		sess.Decisions = sess.Decisions[len(sess.Decisions)-128:]
	}
	for i := range sess.Decisions {
		sess.Decisions[i].Command = redact.RedactSecrets(sess.Decisions[i].Command)
		if len(sess.Decisions[i].Command) > 4096 {
			sess.Decisions[i].Command = sess.Decisions[i].Command[:4096] + "…"
		}
	}
	// External ref URIs commonly carry tokens in query strings.
	if len(sess.ExternalRefs) > 0 {
		sess.ExternalRefs = redactExternalRefs(sess.ExternalRefs)
	}
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
	// Authored-input metadata is a mutable pointer, so it is not covered by
	// the message fingerprint. Prompts below the boundary are skipped only
	// while a keyed digest of every one of them still matches the digest
	// recorded after the previous save; any in-place edit, replacement,
	// reordering or trim changes the digest and redacts them all again.
	// The registry generation is read before the scan: a secret registered
	// while this save runs must not be stamped as already applied.
	promptGen := redact.Generation()
	promptStart := 0
	if boundary > 0 && s.promptsUnchanged(sess.ID, sess.Messages, boundary) {
		promptStart = boundary
	}
	for i := promptStart; i < len(sess.Messages); i++ {
		if sess.Messages[i].PrincipalPrompt != nil {
			s.promptRedactions++
			prompt := redact.RedactSecrets(*sess.Messages[i].PrincipalPrompt)
			sess.Messages[i].PrincipalPrompt = &prompt
		}
	}
	for i := boundary; i < len(sess.Messages); i++ {
		sess.Messages[i].Content = redact.RedactSecrets(sess.Messages[i].Content)
		sess.Messages[i].ReasoningContent = redact.RedactSecrets(sess.Messages[i].ReasoningContent)
		// Tool-call arguments are model-authored (shell commands, headers,
		// file contents) and reach disk verbatim otherwise.
		if len(sess.Messages[i].ToolCalls) > 0 {
			// Copy first: the slice may be shared with the caller's live history.
			calls := append([]ToolCall(nil), sess.Messages[i].ToolCalls...)
			for j := range calls {
				calls[j].Function.Arguments = redactToolArguments(calls[j].Function.Arguments)
			}
			sess.Messages[i].ToolCalls = calls
		}
	}

	// Set the redact boundary (and its fingerprint anchor) BEFORE the
	// marshal: every message below the boundary was redacted by earlier
	// saves and the rest just above, so the boundary is simply the surviving
	// count — and setting it first lets an ordinary (under-cap) save marshal
	// the transcript exactly once instead of twice.
	sess.RedactBoundary = len(sess.Messages)
	sess.RedactBoundaryFP = ""
	if n := len(sess.Messages); n > 0 {
		sess.RedactBoundaryFP = redactMessageFP(sess.Messages[n-1])
	}
	s.marshalCount++
	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}

	// Write-path size cap: MaxSessionFileBytes is enforced at Load, so a
	// session allowed to grow past it on disk would become unloadable. Trim
	// the oldest message groups (keeping the system message at index 0 and
	// the most recent turns, mirroring the loop's trim semantics) until the
	// serialized form fits. This is the rare path: the trim re-marshals
	// internally, then the boundary is recomputed for the shrunken
	// transcript and marshaled once more.
	if len(data) > MaxSessionFileBytes {
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
		// Trimming shrank the transcript and may have inserted a marker:
		// re-anchor the boundary and remarshal (trim's own bytes predate
		// the boundary update).
		sess.RedactBoundary = len(sess.Messages)
		sess.RedactBoundaryFP = ""
		if n := len(sess.Messages); n > 0 {
			sess.RedactBoundaryFP = redactMessageFP(sess.Messages[n-1])
		}
		s.marshalCount++
		data, err = json.Marshal(sess)
		if err != nil {
			return fmt.Errorf("session: marshal: %w", err)
		}
	}

	if err := fsatomic.WriteFile(s.path(sess.ID), data, 0600); err != nil {
		return fmt.Errorf("session: write: %w", err)
	}

	sess.persistedID = sess.ID
	committed = true
	s.rememberRevision(sess)
	s.rememberPrompts(sess.ID, sess.Messages, promptGen)

	// Update the index atomically.
	entry := indexEntry(sess)
	if lazyIndex {
		if old, ok := s.peekIndexEntry(sess.ID); ok && indexMayLag(old, *entry) {
			return nil
		}
	}
	idx := s.loadIndex()
	idx[sess.ID] = entry
	if err := s.saveIndexLocked(idx); err != nil {
		return err
	}

	// Note: the vector index is updated by the caller (addToVectorIndex) after
	// s.mu is released — embedding may be a slow remote call and must not run
	// under the store mutex.

	return nil
}

// protectedHeadLen returns how many leading messages the write-time trim
// keeps: everything up to and including the first user message (the system
// prompt and the original task). Without a user message only a leading system
// message is protected.
func protectedHeadLen(msgs []Message) int {
	for i, m := range msgs {
		if m.Role == "user" && m.Name != ReturnAfterBreakName {
			return i + 1
		}
	}
	if len(msgs) > 0 && msgs[0].Role == "system" {
		return 1
	}
	return 0
}

// trimToFileCapLocked drops the oldest message groups from sess until its
// serialized form fits within MaxSessionFileBytes, returning the trimmed
// JSON. Caller must hold s.mu.
//
// Group semantics mirror the loop's context trimming: the protected head (the
// system message at index 0 and the first principal user message, which holds
// the original task) is kept while anything else can be dropped, and an
// assistant tool_calls message is dropped together with its following tool-result messages so a stored transcript
// never contains orphaned tool messages (which strict providers reject).
// The turn count is recounted to match the surviving messages. When any
// groups were dropped, a marker system message is inserted after the protected
// head so a resumed session can see that earlier history was removed.
// If nothing droppable remains (a degenerate case, e.g. a single oversized
// system message), the session is written as-is — failing the save would
// lose data.
func (s *Store) trimToFileCapLocked(sess *Session, data []byte) ([]byte, error) {
	droppedGroups := 0
	headFallback := false
	for len(data) > MaxSessionFileBytes {
		start := protectedHeadLen(sess.Messages)
		if start >= len(sess.Messages) {
			// Only the protected head remains; the original task goes
			// before the file becomes unloadable.
			start = 0
			if len(sess.Messages) > 0 && sess.Messages[0].Role == "system" {
				start = 1
			}
			headFallback = true
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
		marker := Message{
			Role: "system",
			Content: fmt.Sprintf(
				"[Session storage limit: %d oldest message group(s) were removed from this transcript to stay within the %d-byte file cap. Earlier conversation context is unavailable.]",
				droppedGroups, MaxSessionFileBytes,
			),
		}
		insertAt := protectedHeadLen(sess.Messages)
		if headFallback || insertAt > len(sess.Messages) {
			insertAt = 0
			if len(sess.Messages) > 0 && sess.Messages[0].Role == "system" {
				insertAt = 1
			}
		}
		withMarker := make([]Message, 0, len(sess.Messages)+1)
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
func (s *Store) Load(id string) (_ *Session, loadErr error) {
	defer func() {
		if ValidateSessionID(id) == nil && !os.IsNotExist(loadErr) {
			diagnostics.Report("session", "load", id, loadErr)
		}
	}()
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	fd, err := os.OpenFile(s.path(id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("session: load %q: %w", id, err)
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil {
		return nil, fmt.Errorf("session: load %q: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session: load %q: not a regular file", id)
	}
	if info.Size() > int64(MaxSessionFileBytes) {
		return nil, fmt.Errorf("session: load %q: file too large (%d bytes, max %d)", id, info.Size(), MaxSessionFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(fd, int64(MaxSessionFileBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("session: load %q: %w", id, err)
	}
	if len(data) > MaxSessionFileBytes {
		return nil, fmt.Errorf("session: load %q: file grew beyond cap", id)
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
	sess.persistedID = sess.ID
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
			if limit > 0 && len(live) >= limit {
				break // entries are newest-first: the page is full, stop statting
			}
			if ValidateSessionID(e.ID) != nil {
				continue
			}
			s.listStats.Add(1)
			if _, err := os.Stat(s.path(e.ID)); err != nil {
				continue
			}
			live = append(live, e)
		}
		entries = live

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
	unlock, lockErr := s.fileLock()
	if lockErr != nil {
		s.mu.Unlock()
		return lockErr
	}
	err := s.removeLocked(id)
	if err == nil {
		idx := s.loadIndex()
		delete(idx, id)
		err = s.saveIndexLocked(idx)
	}
	unlock()
	s.mu.Unlock()

	if err == nil && s.OnDelete != nil {
		s.OnDelete(id)
	}
	return err
}

// removeLocked deletes the session FILE and its vector-index entry. The
// store mutex must be held. A missing file is nil (idempotent).
func (s *Store) removeLocked(id string) error {
	if err := s.removeFilesLocked(id); err != nil {
		return err
	}
	// Remove from vector index to prevent stale entries.
	if s.Vec != nil {
		_ = s.Vec.Remove(id) // best-effort
	}
	return nil
}

// removeFilesLocked deletes the session file and its audit log, leaving the
// vector index to the caller (Cleanup batches it outside the locks). A
// missing file is nil (idempotent).
func (s *Store) removeFilesLocked(id string) error {
	delete(s.revStamps, id)
	err := os.Remove(s.path(id))
	if err == nil || os.IsNotExist(err) {
		// The audit log records ingest sources and resources of the session;
		// it must not outlive a session the operator deleted.
		_ = NewAuditStore(s.dir).Remove(id)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// removeVectors drops ids from the vector index with a single store write.
// Best-effort, like every other vector-index removal.
func (s *Store) removeVectors(ids []string) {
	if s.Vec != nil && len(ids) > 0 {
		_ = s.Vec.RemoveMany(ids)
	}
}

// Cleanup deletes all unpinned sessions whose UpdatedAt is before the given
// time. Pinned (favorited) sessions are skipped so operator bookmarks
// survive retention sweeps. Returns the count of deleted sessions.
// Idempotent — nonexistent files are skipped silently.
// Uses the session index for efficient batch operations. Falls back to
// scanning individual session files when no index exists (backward compat).
func (s *Store) Cleanup(before time.Time) (int, error) {
	s.mu.Lock()
	unlock, err := s.fileLock()
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	idx := s.loadIndex()
	if len(idx) > 0 {
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
				if e.Pinned {
					continue
				}
				if err := s.removeFilesLocked(id); err != nil {
					unlock()
					s.mu.Unlock()
					s.removeVectors(cascaded)
					return deleted, fmt.Errorf("session: delete %q: %w", id, err)
				}
				delete(idx, id)
				cascaded = append(cascaded, id)
				deleted++
			}
		}
		if deleted > 0 || purged > 0 {
			if err := s.saveIndexLocked(idx); err != nil {
				unlock()
				s.mu.Unlock()
				s.removeVectors(cascaded)
				return deleted, err
			}
		}
		unlock()
		s.mu.Unlock()
		// Embedding-store rewrites run outside the store mutex and the
		// cross-process lock, in one write, so a large sweep never stalls
		// concurrent saves.
		s.removeVectors(cascaded)
		if s.OnDelete != nil {
			for _, id := range cascaded {
				s.OnDelete(id)
			}
		}
		return deleted, nil
	}
	unlock()
	s.mu.Unlock()

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
			if sess.Pinned {
				continue
			}
			if err := s.Delete(sess.ID); err != nil {
				return deleted, fmt.Errorf("session: delete %q: %w", sess.ID, err)
			}
			deleted++
		}
	}
	return deleted, nil
}

// ── Helpers ────────────────────────────────────────────────────────────

// countUserTurns returns the number of principal user messages in a slice.
// Runtime-injected user messages (background notices and wakes, the
// return-after-break summary) are not principal turns.
func countUserTurns(messages []Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == "user" && !IsSyntheticUserName(m.Name) {
			count++
		}
	}
	return count
}

// GetMessages returns the session's message slice. Nil-safe.
// Returns an empty (non-nil) slice for a session with no messages.
func (s *Session) GetMessages() []Message {
	if s == nil || s.Messages == nil {
		return []Message{}
	}
	return s.Messages
}
