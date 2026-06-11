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
	// Used by the default (RandomProjections) embedder only; HTTP embedders
	// take their dimensionality from the model.
	episodeVectorDim = 256

	episodeVectorFile = "episodes_vectors.gob"
	episodeEmbedFile  = "episodes_embedder.gob"

	// episodeIndexMetaFile records which embedding space the persisted
	// vectors live in. Vectors are only reusable by an embedder with the
	// same fingerprint — switching provider/model/dims forces a rebuild.
	episodeIndexMetaFile = "episodes_index_meta.json"
)

// rebuildRetryInterval is the cool-down after a failed index rebuild. With a
// remote embedding backend, a down server must not be re-hit on every loop
// turn — recall degrades to "no context" until the next attempt.
const rebuildRetryInterval = 30 * time.Second

// scoredEpisode pairs a session ID with its cosine similarity to a query.
type scoredEpisode struct {
	ID    string
	Score float32
}

// episodeVectorIndex is a persisted embedder + brute-force k-NN store for
// per-turn episode recall.
//
// Rationale for dirty-flag + full-rebuild design: the default RandomProjections
// embedder must be Fit on the FULL corpus to build a valid vocabulary —
// embedding incrementally with a stale Fit produces degenerate vectors for new
// terms. Episodes are written at session-end (infrequent); recall fires every
// turn (frequent). The trade-off is one O(n) rebuild after each new episode,
// then fast cached cosine on every subsequent turn until the next write. For
// HTTP embedders the rebuild's network cost is one batch call over texts not
// already in the embedder's cache.
//
// Thread-safety: all exported methods hold vi.mu as appropriate.
// Per-directory singleton: see sharedEpisodeIndex.
type episodeVectorIndex struct {
	mu    sync.RWMutex
	store *vector.Store
	emb   textEmbedder
	// newEmb builds a FRESH embedder instance for a rebuild. Rebuilds embed on
	// a detached instance off-lock (see ensureFresh), so the live (emb, store)
	// pair stays valid and consistent until the atomic swap — a query never
	// embeds against a half-refitted embedder.
	newEmb     func() textEmbedder
	dir        string
	ready      bool
	dirty      bool
	rebuilding bool      // single-flight guard: a rebuild is embedding off-lock
	dirtySeq   uint64    // bumped by markDirty; reconciled after an off-lock rebuild
	failedAt   time.Time // last failed rebuild; zero = never failed
}

// indexMeta is the persisted embedding-space identity (episodeIndexMetaFile).
type indexMeta struct {
	Fingerprint string `json:"fingerprint"`
}

// ── Per-directory singleton ───────────────────────────────────────────────────

// episodeIndexes holds one *episodeVectorIndex per (absolute memory directory,
// embedder fingerprint), shared across all MemoryManager / EpisodeStore
// instances in the process. odek serve builds one manager per WebSocket
// connection — all over the same ~/.odek/memory — so a per-instance index
// would race on the .gob files. The fingerprint is part of the key so a
// process mixing embedding configs over one dir (unusual) gets distinct
// in-memory indexes; the persisted gobs are still guarded by the meta file.
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

// sharedEpisodeIndex returns the process-wide index for dir and the embedding
// space produced by newEmb, creating it on first call. The index is lazily
// initialised on first search.
func sharedEpisodeIndex(dir string, newEmb func() textEmbedder) *episodeVectorIndex {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	emb := newEmb()
	key := abs + "|" + emb.Fingerprint()
	epIdxMu.Lock()
	defer epIdxMu.Unlock()
	if vi, ok := epIdxes[key]; ok {
		return vi
	}
	vi := &episodeVectorIndex{dir: abs, emb: emb, newEmb: newEmb}
	epIdxes[key] = vi
	return vi
}

// ── Public methods ────────────────────────────────────────────────────────────

// markDirty signals that the on-disk episodes changed. The next search will
// rebuild the index before serving results. No disk I/O on this path. A
// pending failure cool-down is cleared: new data is a fresh reason to retry.
func (vi *episodeVectorIndex) markDirty() {
	vi.mu.Lock()
	vi.dirty = true
	vi.dirtySeq++
	vi.failedAt = time.Time{}
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
	vec, err := vi.emb.Embed(query)
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
//
// The expensive part of a rebuild — fitting the embedder and embedding the
// whole corpus, which is a blocking network call for the HTTP backend — runs
// OFF the lock on a fresh embedder instance, then the result is swapped in
// atomically under the lock. This keeps a slow embedding backend from
// serializing every concurrent recall behind one rebuild (search runs per
// turn; under `odek serve` all connections funnel through this per-dir
// singleton). A single-flight guard (rebuilding) collapses a thundering herd
// of concurrent rebuilds into one, and a dirty-sequence check folds in any
// episode write that lands mid-rebuild.
func (vi *episodeVectorIndex) ensureFresh() {
	vi.mu.RLock()
	ready := vi.ready && !vi.dirty
	vi.mu.RUnlock()
	if ready {
		return
	}

	vi.mu.Lock()
	if vi.ready && !vi.dirty {
		vi.mu.Unlock()
		return
	}
	// Back off after a failed rebuild so a down embedding backend is not
	// re-hit on every loop turn (search runs per turn).
	if !vi.failedAt.IsZero() && time.Since(vi.failedAt) < rebuildRetryInterval {
		vi.mu.Unlock()
		return
	}
	// Single-flight: if another goroutine is already rebuilding off-lock, serve
	// the current state for this turn rather than launch a duplicate (and a
	// duplicate batch embed) — the in-flight rebuild will publish shortly.
	if vi.rebuilding {
		vi.mu.Unlock()
		return
	}
	// Cold start without a pending write: try the persisted state first
	// (disk-only, fast — fine to do under the lock).
	if !vi.ready && !vi.dirty {
		if vi.tryLoadLocked() {
			vi.mu.Unlock()
			return
		}
	}
	// Rebuild needed. Snapshot the dirty sequence, take a fresh embedder, and
	// release the lock before the network-bound embedding work.
	vi.rebuilding = true
	seq := vi.dirtySeq
	emb := vi.newEmb()
	vi.mu.Unlock()

	store := buildEpisodeStore(vi.readAllSummaries(), emb)

	vi.mu.Lock()
	defer vi.mu.Unlock()
	vi.rebuilding = false
	if store == nil {
		// Embedding failed (e.g. backend down) — keep the previous index, if
		// any, serving and start the retry cool-down.
		vi.failedAt = time.Now()
		return
	}
	vi.store = store
	vi.emb = emb
	vi.ready = true
	vi.failedAt = time.Time{}
	// Only clear dirty if no write landed while we were rebuilding off-lock;
	// otherwise leave it set so the next search rebuilds with the newest data.
	if vi.dirtySeq == seq {
		vi.dirty = false
	}
	vi.saveLocked()
}

// tryLoadLocked attempts to load persisted state. Returns true on success.
// The persisted vectors are only valid for the embedding space they were
// built in: the meta fingerprint must match the current embedder. A missing
// meta file is treated as the legacy RandomProjections layout and accepted
// only when the current embedder IS that legacy space, so pre-existing
// installs keep their index without a rebuild. Caller must hold vi.mu (write).
func (vi *episodeVectorIndex) tryLoadLocked() bool {
	fp := vi.emb.Fingerprint()
	data, err := os.ReadFile(filepath.Join(vi.dir, episodeIndexMetaFile))
	if err != nil {
		// Legacy layout (no meta): only the original rp/256 space can own it.
		if fp != legacyRPFingerprint() {
			return false
		}
	} else {
		var meta indexMeta
		if json.Unmarshal(data, &meta) != nil || meta.Fingerprint != fp {
			return false
		}
	}

	store := vector.NewStore(vector.CosineDistance)
	if err := store.Load(filepath.Join(vi.dir, episodeVectorFile)); err != nil {
		return false
	}
	if !vi.emb.LoadState(filepath.Join(vi.dir, episodeEmbedFile)) {
		return false
	}
	vi.store = store
	vi.ready = true
	return true
}

// legacyRPFingerprint is the embedding space of indexes persisted before the
// meta file existed (RandomProjections at episodeVectorDim).
func legacyRPFingerprint() string {
	return newRPTextEmbedder(episodeVectorDim).Fingerprint()
}

// buildEpisodeStore fits emb on the full corpus and returns a populated vector
// store, or nil on any embedding failure (e.g. a remote backend being down) so
// the caller can keep the previous index serving. It takes NO lock and touches
// no shared index state — it operates entirely on its arguments and a fresh
// embedder, which is what lets ensureFresh run it off vi.mu.
func buildEpisodeStore(texts []idText, emb textEmbedder) *vector.Store {
	corpus := make([]string, len(texts))
	for i, t := range texts {
		corpus[i] = t.text
	}

	if err := emb.Fit(corpus); err != nil {
		return nil
	}
	vecs, err := emb.EmbedAll(corpus)
	if err != nil {
		return nil
	}

	store := vector.NewStore(vector.CosineDistance)
	for i, t := range texts {
		if vecs[i] == nil {
			continue
		}
		store.Add(t.id, vecs[i])
	}
	return store
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

// saveLocked atomically persists the store, the embedder state (RP only —
// stateless embedders no-op), and the embedding-space meta file. Caller must
// hold vi.mu (write lock). Fixed temp names are safe because the index is a
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
	vi.emb.SaveState(filepath.Join(vi.dir, episodeEmbedFile))

	metaPath := filepath.Join(vi.dir, episodeIndexMetaFile)
	if data, err := json.Marshal(indexMeta{Fingerprint: vi.emb.Fingerprint()}); err == nil {
		tmp := metaPath + ".tmp"
		if os.WriteFile(tmp, data, 0600) == nil {
			if err := os.Rename(tmp, metaPath); err != nil {
				os.Remove(tmp)
			}
		}
	}
}
