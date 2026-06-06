package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

const (
	// episodeVectorDim matches the session and fact-merge embedders (256).
	episodeVectorDim = 256

	episodeVectorFile = "episodes_vectors.gob"
	episodeEmbedFile  = "episodes_embedder.gob"
)

// scoredEpisode pairs a session ID with its cosine similarity to a query.
type scoredEpisode struct {
	ID    string
	Score float32
}

// episodeVectorIndex is a persisted go-vector RandomProjections embedder +
// brute-force k-NN store for per-turn episode recall.
//
// Rationale for dirty-flag + full-rebuild design: go-vector's RandomProjections
// must be Fit on the FULL corpus to build a valid vocabulary — embedding
// incrementally with a stale Fit produces degenerate vectors for new terms.
// Episodes are written at session-end (infrequent); recall fires every turn
// (frequent). The trade-off is one O(n) rebuild after each new episode, then
// fast cached cosine on every subsequent turn until the next write.
//
// Thread-safety: all exported methods hold vi.mu as appropriate.
// Per-directory singleton: see sharedEpisodeIndex.
type episodeVectorIndex struct {
	mu    sync.RWMutex
	store *vector.Store
	emb   *vector.RandomProjections
	dir   string
	ready bool
	dirty bool
}

// ── Per-directory singleton ───────────────────────────────────────────────────

// episodeIndexes holds one *episodeVectorIndex per absolute memory directory,
// shared across all MemoryManager / EpisodeStore instances in the process.
// odek serve builds one manager per WebSocket connection — all over the same
// ~/.odek/memory — so a per-instance index would race on the .gob files.
//
// Multi-process note: two separate odek processes sharing the same memory
// directory are NOT serialized by this in-process singleton. Concurrent saves
// from distinct processes can interleave on the .tmp files. The practical
// impact is limited — the worst case is one process loading a recently-rebuilt
// gob pair and getting slightly stale recall — but operators running multiple
// odek processes against a shared ~/.odek/memory should be aware of this.
// Each process still produces internally-consistent gob pairs (store+emb are
// rebuilt atomically within one process); the risk is only cross-process.
var (
	epIdxMu sync.Mutex
	epIdxes = map[string]*episodeVectorIndex{}
)

// sharedEpisodeIndex returns the process-wide index for dir, creating it on
// first call. The index is lazily initialised on first search.
func sharedEpisodeIndex(dir string) *episodeVectorIndex {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	epIdxMu.Lock()
	defer epIdxMu.Unlock()
	if vi, ok := epIdxes[abs]; ok {
		return vi
	}
	vi := &episodeVectorIndex{dir: abs}
	epIdxes[abs] = vi
	return vi
}

// ── Public methods ────────────────────────────────────────────────────────────

// markDirty signals that the on-disk episodes changed. The next search will
// rebuild the index before serving results. No disk I/O on this path.
func (vi *episodeVectorIndex) markDirty() {
	vi.mu.Lock()
	vi.dirty = true
	vi.mu.Unlock()
}

// search rebuilds if needed, embeds the query, and returns up to k nearest
// episodes by cosine similarity. Returns nil if the index is empty or an error
// occurs — callers treat this as "no context available" and carry on.
func (vi *episodeVectorIndex) search(query string, k int) []scoredEpisode {
	vi.ensureFresh()
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	if !vi.ready || vi.store == nil || vi.store.Len() == 0 || vi.emb == nil {
		return nil
	}
	if k <= 0 {
		k = 5
	}
	vec, err := vi.emb.Embed(featurizeForEmbedding(query))
	if err != nil {
		return nil
	}
	res := vi.store.Search(vec, k)
	out := make([]scoredEpisode, 0, len(res))
	for _, r := range res {
		out = append(out, scoredEpisode{ID: r.ID, Score: 1 - r.Distance})
	}
	return out
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// ensureFresh loads or rebuilds the index as needed. Must NOT be called while
// holding vi.mu (it acquires it internally).
func (vi *episodeVectorIndex) ensureFresh() {
	vi.mu.RLock()
	if vi.ready && !vi.dirty {
		vi.mu.RUnlock()
		return
	}
	vi.mu.RUnlock()

	vi.mu.Lock()
	defer vi.mu.Unlock()
	if vi.ready && !vi.dirty {
		return // double-checked
	}

	// Cold start without a pending write: try the persisted gobs first.
	if !vi.ready && !vi.dirty {
		if vi.tryLoadLocked() {
			return
		}
	}
	// Either cold-start without gobs, or dirty after a write — full rebuild.
	vi.rebuildLocked()
}

// tryLoadLocked attempts to load persisted state. Returns true on success.
// Caller must hold vi.mu (write lock).
func (vi *episodeVectorIndex) tryLoadLocked() bool {
	store := vector.NewStore(vector.CosineDistance)
	if err := store.Load(filepath.Join(vi.dir, episodeVectorFile)); err != nil {
		return false
	}
	emb, err := vector.LoadEmbedder(filepath.Join(vi.dir, episodeEmbedFile))
	if err != nil {
		return false
	}
	vi.store = store
	vi.emb = emb
	vi.ready = true
	return true
}

// rebuildLocked reads all episode summaries from disk, fits the RP embedder on
// the full corpus, and persists the result. Caller must hold vi.mu (write lock).
func (vi *episodeVectorIndex) rebuildLocked() {
	texts := vi.readAllSummaries()

	corpus := make([]string, len(texts))
	for i, t := range texts {
		corpus[i] = featurizeForEmbedding(t.text)
	}

	emb := vector.NewRandomProjections(episodeVectorDim)
	emb.Fit(corpus)

	store := vector.NewStore(vector.CosineDistance)
	for i, t := range texts {
		vec, err := emb.Embed(corpus[i])
		if err != nil {
			continue
		}
		store.Add(t.id, vec)
	}

	vi.store = store
	vi.emb = emb
	vi.ready = true
	vi.dirty = false
	vi.saveLocked()
}

type idText struct {
	id   string
	text string
}

// readAllSummaries reads the JSON episode index and then the full on-disk
// summary for each entry. Unreadable entries are silently skipped.
func (vi *episodeVectorIndex) readAllSummaries() []idText {
	// Parse index.json directly — avoids importing EpisodeStore / circular deps.
	type meta struct {
		SessionID string    `json:"session_id"`
		CreatedAt time.Time `json:"created_at"`
	}
	data, err := os.ReadFile(filepath.Join(vi.dir, episodeIndexFile))
	if err != nil {
		return nil
	}
	var index []meta
	if err := json.Unmarshal(data, &index); err != nil {
		return nil
	}
	out := make([]idText, 0, len(index))
	for _, m := range index {
		path := filepath.Join(vi.dir, m.SessionID+".md")
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(b))
		if text == "" {
			continue
		}
		out = append(out, idText{id: m.SessionID, text: text})
	}
	return out
}

// saveLocked atomically persists the store and embedder. Caller must hold
// vi.mu (write lock). Fixed temp names are safe because the index is a
// per-dir singleton, so all saves funnel through this mutex-guarded method.
func (vi *episodeVectorIndex) saveLocked() {
	if vi.store == nil || vi.emb == nil || vi.dir == "" {
		return
	}
	storePath := filepath.Join(vi.dir, episodeVectorFile)
	if tmp := storePath + ".tmp"; vi.store.Save(tmp) == nil {
		if err := os.Rename(tmp, storePath); err != nil {
			os.Remove(tmp)
		}
	}
	embPath := filepath.Join(vi.dir, episodeEmbedFile)
	if tmp := embPath + ".tmp"; vi.emb.SaveEmbedder(tmp) == nil {
		if err := os.Rename(tmp, embPath); err != nil {
			os.Remove(tmp)
		}
	}
}
