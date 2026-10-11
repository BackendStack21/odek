package extended

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
)

const (
	vectorFile     = "vectors.gob"
	vectorMetaFile = "vectors_meta.json"
	vectorDim      = 256
	retryInterval  = 30 * time.Second
)

// textEmbedder is the local alias for the shared embedding seam.
type textEmbedder = embedding.TextEmbedder

// scoredAtom pairs an atom ID with its similarity score.
type scoredAtom struct {
	ID    string
	Score float32
}

// vectorMeta records the embedding-space fingerprint of the persisted vectors
// plus a fingerprint of the atom corpus they were built from. The corpus
// fingerprint detects a stale index at load time (atoms added or removed
// after the last persist); it is empty in files written before it was
// introduced, which is treated as a mismatch so the index rebuilds once.
type vectorMeta struct {
	Fingerprint string `json:"fingerprint"`
	Corpus      string `json:"corpus,omitempty"`
}

// corpusFingerprint returns a cheap fingerprint of the atom corpus: the atom
// count plus an FNV-1a hash of the sorted atom IDs.
func corpusFingerprint(atoms []MemoryAtom) string {
	ids := make([]string, 0, len(atoms))
	for _, a := range atoms {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%d:%016x", len(ids), h.Sum64())
}

// atomVectorIndex is a persisted embedder + brute-force k-NN store for atom
// recall. It rebuilds from disk when dirty and caches the result.
type atomVectorIndex struct {
	mu     sync.RWMutex
	wg     sync.WaitGroup
	dir    string
	store  *vector.Store
	emb    textEmbedder
	newEmb func() textEmbedder
	ready  bool
	dirty  bool

	rebuilding bool
	done       *sync.Cond
	dirtySeq   uint64
	failedAt   time.Time

	listAtoms func() ([]MemoryAtom, error)
}

// newAtomVectorIndex creates an index rooted at dir. listAtoms provides the
// current atom set for rebuilds.
func newAtomVectorIndex(dir string, newEmb func() textEmbedder, listAtoms func() ([]MemoryAtom, error)) *atomVectorIndex {
	if newEmb == nil {
		newEmb = func() textEmbedder { return embedding.New(nil, vectorDim) }
	}
	vi := &atomVectorIndex{
		dir:       dir,
		emb:       newEmb(),
		newEmb:    newEmb,
		listAtoms: listAtoms,
	}
	vi.done = sync.NewCond(&vi.mu)
	return vi
}

// markDirty signals that the atom corpus changed and the index must rebuild.
func (vi *atomVectorIndex) markDirty() {
	vi.mu.Lock()
	vi.dirty = true
	vi.dirtySeq++
	vi.failedAt = time.Time{}
	vi.mu.Unlock()
}

// search returns up to k atom IDs ranked by cosine similarity to the query.
func (vi *atomVectorIndex) search(query string, k int) []scoredAtom {
	vi.ensureFresh()
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.searchLocked(query, k)
}

// searchCurrent queries the index in its current state without triggering a
// rebuild. Semantic dedup inside addAtoms uses it so the one-rebuild-per-batch
// invariant holds even when the index went stale mid-batch (e.g. after a
// cap-enforcement eviction).
func (vi *atomVectorIndex) searchCurrent(query string, k int) []scoredAtom {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.searchLocked(query, k)
}

// searchLocked implements search. Caller must hold vi.mu (read or write).
func (vi *atomVectorIndex) searchLocked(query string, k int) []scoredAtom {
	if !vi.ready || vi.store == nil || vi.store.Len() == 0 || vi.emb == nil {
		return nil
	}
	if k <= 0 {
		k = 10
	}
	vec, err := vi.emb.Embed(query)
	if err != nil {
		log.Printf("extended memory: embedding query failed: %v", err)
		return nil
	}
	res := vi.store.Search(vec, k)
	out := make([]scoredAtom, 0, len(res))
	for _, r := range res {
		out = append(out, scoredAtom{ID: r.ID, Score: 1 - r.Distance})
	}
	return out
}

// similarity returns the cosine similarity between two texts embedded with
// the index's shared embedder, or 0 when no embedder is available. It is used
// to compare atoms that are not yet indexed (e.g. stored earlier in the same
// add batch).
func (vi *atomVectorIndex) similarity(a, b string) float32 {
	vi.mu.RLock()
	emb := vi.emb
	vi.mu.RUnlock()
	if emb == nil {
		return 0
	}
	va, err := emb.Embed(a)
	if err != nil {
		return 0
	}
	vb, err := emb.Embed(b)
	if err != nil {
		return 0
	}
	return cosine(va, vb)
}

// embedTexts embeds a batch of texts with the index's shared embedder.
func (vi *atomVectorIndex) embedTexts(texts []string) ([]vector.Vector, error) {
	vi.mu.RLock()
	emb := vi.emb
	vi.mu.RUnlock()
	if emb == nil {
		return nil, fmt.Errorf("extended memory: no embedder available")
	}
	return emb.EmbedAll(texts)
}

// stats reports the number of indexed vectors and whether the index needs a
// rebuild (dirty or never built).
func (vi *atomVectorIndex) stats() (vectors int, dirty bool) {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	if vi.store != nil {
		vectors = vi.store.Len()
	}
	return vectors, vi.dirty || !vi.ready
}

// sharedRefitSafe is implemented by embedders whose Fit and LoadState may run
// while other goroutines embed with the same instance: stateless, internally
// locked backends such as the HTTP embedder, whose vectors do not depend on
// the fit.
type sharedRefitSafe interface{ SharedRefitSafe() bool }

// rebuildEmbedder returns the embedder instance a rebuild or load should fit:
// the live instance when it is safe to refit while shared (so the HTTP
// embedder keeps its text→vector cache and a re-fit of a mostly-unchanged
// corpus stays cheap), otherwise a fresh one. The local RandomProjections
// embedder has no internal locking and refits its vocabulary in place, so
// fitting the live instance would race with queries that embed with it
// (searchCurrent under the read lock, similarity and embedTexts unlocked)
// and embed them against a half-fitted vocabulary. Caller must hold vi.mu.
func (vi *atomVectorIndex) rebuildEmbedder() textEmbedder {
	if vi.emb == nil {
		return vi.newEmb()
	}
	if s, ok := vi.emb.(sharedRefitSafe); ok && s.SharedRefitSafe() {
		return vi.emb
	}
	return vi.newEmb()
}

// ensureFresh rebuilds the index if needed. The expensive embedding work runs
// off-lock on the instance rebuildEmbedder picks, and the fitted embedder is
// swapped in together with the store it produced, so readers always embed
// with the embedder that built the live vectors. Concurrent callers wait for
// the first rebuild rather than starting redundant work.
func (vi *atomVectorIndex) ensureFresh() {
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
	if !vi.failedAt.IsZero() && time.Since(vi.failedAt) < retryInterval {
		vi.mu.Unlock()
		return
	}
	if vi.rebuilding {
		// Wait for the in-flight rebuild to finish.
		for vi.rebuilding {
			vi.done.Wait()
		}
		vi.mu.Unlock()
		return
	}
	if !vi.ready && !vi.dirty {
		if vi.tryLoadLocked() {
			vi.mu.Unlock()
			return
		}
	}

	vi.rebuilding = true
	seq := vi.dirtySeq
	emb := vi.rebuildEmbedder()
	listFn := vi.listAtoms
	vi.mu.Unlock()

	store, corpusFP := vi.build(emb, listFn)

	vi.mu.Lock()
	defer vi.mu.Unlock()
	vi.rebuilding = false
	vi.done.Broadcast()
	if store == nil {
		vi.failedAt = time.Now()
		return
	}
	vi.store = store
	vi.emb = emb
	vi.ready = true
	vi.failedAt = time.Time{}
	if vi.dirtySeq == seq {
		vi.dirty = false
	}
	vi.persistLocked(corpusFP)
}

// build fits the embedder on the current atom corpus and returns a populated
// vector store plus the corpus fingerprint, or nil on embedding failure.
func (vi *atomVectorIndex) build(emb textEmbedder, listAtoms func() ([]MemoryAtom, error)) (*vector.Store, string) {
	atoms, err := listAtoms()
	if err != nil {
		log.Printf("extended memory: index build failed listing atoms: %v", err)
		return nil, ""
	}
	fp := corpusFingerprint(atoms)
	if len(atoms) == 0 {
		return vector.NewStore(vector.CosineDistance), fp
	}
	corpus := make([]string, len(atoms))
	for i, a := range atoms {
		corpus[i] = a.Text
	}
	if err := emb.Fit(corpus); err != nil {
		log.Printf("extended memory: embedder Fit failed: %v", err)
		return nil, ""
	}
	vecs, err := emb.EmbedAll(corpus)
	if err != nil {
		log.Printf("extended memory: EmbedAll failed: %v", err)
		return nil, ""
	}
	store := vector.NewStore(vector.CosineDistance)
	for i, a := range atoms {
		if vecs[i] == nil {
			continue
		}
		store.Add(a.ID, vecs[i])
	}
	return store, fp
}

// Compact rebuilds the persisted vector store from the current atom corpus in
// the background, reclaiming space from deleted/evicted atoms.
func (vi *atomVectorIndex) Compact() {
	vi.mu.Lock()
	vi.dirty = true
	vi.dirtySeq++
	vi.mu.Unlock()
	vi.wg.Add(1)
	go func() {
		defer vi.wg.Done()
		vi.ensureFresh()
		log.Printf("extended memory: vector index compacted")
	}()
}

// Wait blocks until in-flight background compaction goroutines finish.
func (vi *atomVectorIndex) Wait() {
	if vi == nil {
		return
	}
	vi.wg.Wait()
}

// tryLoadLocked attempts to load persisted state. Caller must hold vi.mu.
func (vi *atomVectorIndex) tryLoadLocked() bool {
	fp := vi.emb.Fingerprint()
	data, err := os.ReadFile(filepath.Join(vi.dir, vectorMetaFile))
	if err != nil {
		return false
	}
	var meta vectorMeta
	if json.Unmarshal(data, &meta) != nil || meta.Fingerprint != fp {
		return false
	}
	// The persisted vectors must match the current atom corpus. A missing
	// corpus fingerprint (file written before it was tracked) or a mismatch
	// means the index is stale and must rebuild once.
	atoms, err := vi.listAtoms()
	if err != nil || meta.Corpus == "" || meta.Corpus != corpusFingerprint(atoms) {
		return false
	}
	store := vector.NewStore(vector.CosineDistance)
	if err := store.Load(filepath.Join(vi.dir, vectorFile)); err != nil {
		return false
	}
	// Load into the instance rebuildEmbedder picks so a reader embedding
	// with the live instance off-lock never sees its state replaced.
	emb := vi.rebuildEmbedder()
	if !emb.LoadState(filepath.Join(vi.dir, vectorFile+".emb")) {
		return false
	}
	vi.store = store
	vi.emb = emb
	vi.ready = true
	return true
}

// persistLocked saves the vector store and embedding-space meta. Caller must
// hold vi.mu.
func (vi *atomVectorIndex) persistLocked(corpusFP string) {
	if vi.store == nil || vi.emb == nil || vi.dir == "" {
		return
	}
	if err := os.MkdirAll(vi.dir, 0700); err != nil {
		return
	}
	storePath := filepath.Join(vi.dir, vectorFile)
	if tmp := storePath + ".tmp"; vi.store.Save(tmp) == nil {
		if err := os.Rename(tmp, storePath); err != nil {
			os.Remove(tmp)
		} else {
			_ = os.Chmod(storePath, 0600)
		}
	}
	embPath := filepath.Join(vi.dir, vectorFile+".emb")
	vi.emb.SaveState(embPath)
	_ = os.Chmod(embPath, 0600)
	if data, err := json.Marshal(vectorMeta{Fingerprint: vi.emb.Fingerprint(), Corpus: corpusFP}); err == nil {
		tmp := filepath.Join(vi.dir, vectorMetaFile+".tmp")
		if os.WriteFile(tmp, data, 0600) == nil {
			if err := os.Rename(tmp, filepath.Join(vi.dir, vectorMetaFile)); err != nil {
				os.Remove(tmp)
			} else {
				_ = os.Chmod(filepath.Join(vi.dir, vectorMetaFile), 0600)
			}
		}
	}
}

// Size returns the on-disk size of the vector index.
func (vi *atomVectorIndex) Size() int64 {
	var total int64
	for _, name := range []string{vectorFile, vectorFile + ".emb", vectorMetaFile} {
		info, err := os.Stat(filepath.Join(vi.dir, name))
		if err == nil {
			total += info.Size()
		}
	}
	return total
}

// cosine computes cosine similarity between two vectors.
func cosine(a, b vector.Vector) float32 {
	return embedding.Cosine(a, b)
}
