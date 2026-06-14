package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/llm"
)

// ── Constants ─────────────────────────────────────────────────────────────

const (
	// vectorDim is the output dimensionality for the default RandomProjections
	// backend. 256 dims balances search quality with memory/CPU for ~100K
	// sessions. HTTP embedders take their dimensionality from the model.
	vectorDim = 256

	// vectorFile is the persisted vector store filename.
	vectorFile = "vectors.gob"

	// embedderFile is the persisted RandomProjections state filename. Stateless
	// (HTTP) backends do not write it.
	embedderFile = "embedder.gob"

	// vectorMetaFile records which embedding space the persisted vectors live
	// in. Vectors are only reusable by an embedder with the same fingerprint —
	// switching provider/model/dims forces a rebuild in the new space.
	vectorMetaFile = "vectors_meta.json"
)

// rebuildRetryInterval is the cool-down after a failed index rebuild. With a
// remote embedding backend, a down server must not be re-hit on every search
// or session save — search degrades to the keyword fallback until the next
// attempt. Mirrors the memory episode index.
const rebuildRetryInterval = 30 * time.Second

// indexMeta is the persisted embedding-space identity (vectorMetaFile).
type indexMeta struct {
	Fingerprint string `json:"fingerprint"`
}

// ── Vector Index ──────────────────────────────────────────────────────────

// VectorIndex provides semantic session search over the shared embedding
// backend (internal/embedding): RandomProjections by default, or any
// OpenAI-compatible HTTP embeddings API when configured.
//
// Lifecycle:
//  1. Init / InitWithConfig loads persisted state (when the fingerprint
//     matches), or fits+embeds from all sessions.
//  2. On Add: embed conversation text and insert into the store.
//  3. On Search: embed query, k-NN search, return ranked results.
//  4. On Remove: delete from the store.
//  5. Save persists store + embedder state + meta atomically.
//
// Resilience: with a remote backend, a down server never fails a session save
// or surfaces an error to search — Add/Search degrade silently and a 30s
// cool-down (failedAt) keeps the backend from being hammered. The keyword
// fallback in the session_search tool is the safety net while the embedder is
// unavailable.
//
// Thread-safe: all exported methods hold the RWMutex.
type VectorIndex struct {
	mu       sync.RWMutex
	store    *vector.Store
	emb      embedding.TextEmbedder
	cfg      *embedding.Config
	dir      string
	ready    bool
	failedAt time.Time // last failed rebuild; zero = never failed
}

// ── Init ──────────────────────────────────────────────────────────────────

// Init creates or loads the vector index using the default RandomProjections
// backend. Equivalent to InitWithConfig(dir, nil).
func (vi *VectorIndex) Init(dir string) error {
	return vi.InitWithConfig(dir, nil)
}

// InitWithConfig creates or loads the vector index using the embedding backend
// selected by cfg (nil = default RandomProjections). If persisted state in the
// same embedding space exists it is loaded directly; otherwise the index is
// rebuilt from all session files. A backend that is down at init time is not
// fatal — the index stays empty (search falls back to keyword) and retries
// after the cool-down.
func (vi *VectorIndex) InitWithConfig(dir string, cfg *embedding.Config) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	vi.dir = dir
	vi.cfg = cfg
	vi.emb = embedding.New(cfg, vectorDim)

	if vi.tryLoadLocked() {
		return nil
	}
	return vi.rebuildLocked()
}

// tryLoadLocked loads persisted state when the meta fingerprint matches the
// current embedder. A missing meta file is treated as an incompatible (legacy
// or unknown) layout and rejected, forcing a one-time rebuild — the persisted
// vectors may have been built in a different embedding space. Caller holds the
// write lock.
func (vi *VectorIndex) tryLoadLocked() bool {
	fp := vi.emb.Fingerprint()
	data, err := os.ReadFile(filepath.Join(vi.dir, vectorMetaFile))
	if err != nil {
		return false
	}
	var meta indexMeta
	if json.Unmarshal(data, &meta) != nil || meta.Fingerprint != fp {
		return false
	}

	store := vector.NewStore(vector.CosineDistance)
	if err := store.Load(filepath.Join(vi.dir, vectorFile)); err != nil {
		return false
	}
	if !vi.emb.LoadState(filepath.Join(vi.dir, embedderFile)) {
		return false
	}
	vi.store = store
	vi.ready = true
	return true
}

// rebuildLocked scans all session files, fits the embedder, batch-embeds every
// session's conversation text, and persists the result. Embedding failures
// (e.g. a remote backend being down) are non-fatal: the index is left not-ready
// and a retry cool-down starts so the backend is not hammered. Caller holds the
// write lock.
func (vi *VectorIndex) rebuildLocked() error {
	// Back off after a failure so a down backend is not re-hit on every search
	// or session save.
	if !vi.failedAt.IsZero() && time.Since(vi.failedAt) < rebuildRetryInterval {
		return nil
	}

	entries, err := os.ReadDir(vi.dir)
	if err != nil {
		return fmt.Errorf("vector: read dir: %w", err)
	}

	var ids []string
	var corpus []string
	for _, e := range entries {
		if e.IsDir() || !isSessionFile(e.Name()) {
			continue
		}

		// Only load session files whose base name is a valid session ID and
		// that are not symlinks. This prevents a planted symlink named like a
		// session file from pointing outside the directory and having its
		// content embedded into the search corpus.
		id := idFromPath(e.Name())
		if err := ValidateSessionID(id); err != nil {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}

		path := filepath.Join(vi.dir, e.Name())
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := extractConversationText(data)
		if text == "" {
			continue
		}
		ids = append(ids, id)
		corpus = append(corpus, text)
	}

	// Fit + batch-embed. For the HTTP backend this is a single batch call over
	// texts not already cached; for RP it is local CPU work.
	if err := vi.emb.Fit(corpus); err != nil {
		vi.failedAt = time.Now()
		return nil
	}
	vecs, err := vi.emb.EmbedAll(corpus)
	if err != nil {
		vi.failedAt = time.Now()
		return nil
	}

	store := vector.NewStore(vector.CosineDistance)
	for i, id := range ids {
		if vecs[i] == nil {
			continue
		}
		store.Add(id, vecs[i])
	}

	vi.store = store
	vi.ready = true
	vi.failedAt = time.Time{}
	return vi.saveLocked()
}

// Ready returns true if the index has been initialized.
func (vi *VectorIndex) Ready() bool {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.ready
}

// ── Mutation ──────────────────────────────────────────────────────────────

// Add embeds the conversation text and adds the session to the index,
// replacing any existing entry. Best-effort: a missing/failing embedding
// backend never fails the caller's session save — the entry is simply skipped
// and a retry cool-down starts. If the index was not ready (e.g. the backend
// was down at init), a rebuild is attempted first; it already picks up the
// just-saved session from disk.
func (vi *VectorIndex) Add(sessionID string, messages []llm.Message) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	if !vi.ready {
		// The new session is already on disk, so a successful rebuild indexes
		// it. If the rebuild can't run (backoff) or fails, skip silently.
		_ = vi.rebuildLocked()
		return nil
	}

	text := BuildConversationText(messages)
	if text == "" {
		return nil
	}

	vec, err := vi.emb.Embed(text)
	if err != nil {
		// Don't fail the session save because the embedding backend is down;
		// start the cool-down so the next operation doesn't hammer it.
		vi.failedAt = time.Now()
		return nil
	}

	// Replace if exists.
	vi.store.Remove(sessionID)
	vi.store.Add(sessionID, vec)

	return vi.saveLocked()
}

// Remove deletes a session from the index. Idempotent.
func (vi *VectorIndex) Remove(sessionID string) error {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	if !vi.ready {
		return nil
	}

	vi.store.Remove(sessionID)
	return vi.saveLocked()
}

// ── Search ────────────────────────────────────────────────────────────────

// SearchResult holds a single session search result.
type SearchResult struct {
	SessionID string  `json:"session_id"`
	Score     float32 `json:"score"` // cosine similarity, higher = more relevant
}

// Search embeds the query and returns the k most similar sessions ranked by
// cosine similarity. Returns nil (no error) when the index is unavailable —
// not ready, empty, or the embedding backend failed — so the caller falls back
// to keyword search. If the index was not ready, one rebuild is attempted
// (subject to the cool-down).
func (vi *VectorIndex) Search(query string, k int) ([]SearchResult, error) {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	if !vi.ready {
		_ = vi.rebuildLocked()
	}
	if !vi.ready || vi.store == nil || vi.store.Len() == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = 5
	}
	if k > 20 {
		k = 20
	}

	vec, err := vi.emb.Embed(query)
	if err != nil {
		// Degrade to the keyword fallback rather than surfacing an error.
		vi.failedAt = time.Now()
		return nil, nil
	}

	// go-vector returns distance = 1 - cosine similarity; convert back.
	results := vi.store.Search(vec, k)
	if len(results) == 0 {
		return nil, nil
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			SessionID: r.ID,
			Score:     1 - r.Distance,
		}
	}
	return out, nil
}

// ── Persistence ───────────────────────────────────────────────────────────

// Save persists the store, embedder state, and meta to disk.
func (vi *VectorIndex) Save() error {
	vi.mu.Lock()
	defer vi.mu.Unlock()
	return vi.saveLocked()
}

// saveLocked atomically persists the vector store, the embedder state (RP only
// — stateless backends no-op), and the embedding-space meta file. Caller holds
// the write lock.
func (vi *VectorIndex) saveLocked() error {
	if !vi.ready || vi.dir == "" {
		return nil
	}

	// Save vector store.
	storePath := filepath.Join(vi.dir, vectorFile)
	tmpStore := storePath + ".tmp"
	if err := vi.store.Save(tmpStore); err != nil {
		os.Remove(tmpStore)
		return fmt.Errorf("vector: save store: %w", err)
	}
	if err := os.Rename(tmpStore, storePath); err != nil {
		os.Remove(tmpStore)
		return fmt.Errorf("vector: rename store: %w", err)
	}

	// Save embedder state (RP writes the gob; HTTP is a no-op).
	vi.emb.SaveState(filepath.Join(vi.dir, embedderFile))

	// Save the embedding-space meta so a later init can validate compatibility.
	metaPath := filepath.Join(vi.dir, vectorMetaFile)
	if data, err := json.Marshal(indexMeta{Fingerprint: vi.emb.Fingerprint()}); err == nil {
		tmp := metaPath + ".tmp"
		if os.WriteFile(tmp, data, 0600) == nil {
			if err := os.Rename(tmp, metaPath); err != nil {
				os.Remove(tmp)
			}
		}
	}

	return nil
}

// ── Conversation Text Extraction ──────────────────────────────────────────

// BuildConversationText extracts user and assistant text from messages
// for embedding. Tool calls and results are excluded — they add noise.
func BuildConversationText(messages []llm.Message) string {
	var out string
	for _, m := range messages {
		switch m.Role {
		case "user":
			if m.Content != "" {
				out += "[User] " + m.Content + "\n"
			}
		case "assistant":
			if m.Content != "" {
				out += "[Assistant] " + m.Content + "\n"
			}
		}
	}
	return out
}

// extractConversationText parses raw JSON session bytes and extracts
// user+assistant text. Used during initial index rebuild before the
// session.Store.Load path is available for bulk operations.
func extractConversationText(data []byte) string {
	var raw struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	var out string
	for _, m := range raw.Messages {
		switch m.Role {
		case "user":
			if m.Content != "" {
				out += "[User] " + m.Content + "\n"
			}
		case "assistant":
			if m.Content != "" {
				out += "[Assistant] " + m.Content + "\n"
			}
		}
	}
	return out
}
