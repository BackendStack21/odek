package extended

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
)

func TestVectorIndexPersistence(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello world", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()

	if !vi.ready {
		t.Fatal("expected index to be ready after first build")
	}
	for _, name := range []string{vectorFile, vectorFile + ".emb", vectorMetaFile} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected persisted file %s: %v", name, err)
		}
		info, _ := os.Stat(path)
		if info != nil && info.Mode().Perm() != 0600 {
			t.Errorf("file %s mode = %04o, want 0600", name, info.Mode().Perm())
		}
	}

	// Re-create index and ensure it loads from disk without rebuilding.
	vi2 := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi2.emb = newMockEmbedder(vectorDim)
	vi2.ensureFresh()
	if !vi2.ready {
		t.Fatal("expected index to load from persisted state")
	}
}

func TestVectorIndexMarkDirtyKeepsFlag(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.mu.RLock()
	dirty := vi.dirty
	seq := vi.dirtySeq
	vi.mu.RUnlock()
	if !dirty {
		t.Error("expected dirty to be true after markDirty")
	}

	vi.ensureFresh()
	vi.mu.RLock()
	dirty = vi.dirty
	newSeq := vi.dirtySeq
	vi.mu.RUnlock()
	if dirty {
		t.Error("expected dirty to be cleared after ensureFresh")
	}
	if newSeq != seq {
		t.Error("expected dirtySeq to remain unchanged after no concurrent marks")
	}
}

func TestVectorIndexDirtyAfterConcurrentMark(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()
	// markDirty again after build should set dirty true even though ready=true.
	vi.markDirty()
	vi.mu.RLock()
	dirty := vi.dirty
	vi.mu.RUnlock()
	if !dirty {
		t.Error("expected dirty to be true after post-build markDirty")
	}
}

func TestVectorIndexFailedRebuildRetry(t *testing.T) {
	dir := t.TempDir()
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return nil, os.ErrNotExist })
	vi.emb = newMockEmbedder(vectorDim)

	vi.ensureFresh()
	vi.mu.RLock()
	failed := !vi.failedAt.IsZero()
	vi.mu.RUnlock()
	if !failed {
		t.Error("expected failedAt to be set after failed rebuild")
	}

	// Ensure retry is skipped within the retry interval.
	vi2 := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return nil, os.ErrNotExist })
	vi2.mu.Lock()
	vi2.failedAt = time.Now().Add(-1 * time.Second)
	vi2.mu.Unlock()
	vi2.ensureFresh()
	vi2.mu.RLock()
	stillNotReady := !vi2.ready
	vi2.mu.RUnlock()
	if !stillNotReady {
		t.Error("expected index to stay unready within retry interval")
	}
}

func TestVectorIndexRebuildAfterRetryInterval(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	vi.mu.Lock()
	vi.failedAt = time.Now().Add(-retryInterval - time.Second)
	vi.mu.Unlock()
	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()
	if !vi.ready {
		t.Fatal("expected index to rebuild after retry interval")
	}
}

func TestVectorIndexSearchEmptyStore(t *testing.T) {
	dir := t.TempDir()
	vi := newAtomVectorIndex(dir, func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }, func() ([]MemoryAtom, error) { return nil, nil })
	vi.emb = newMockEmbedder(vectorDim)
	res := vi.search("anything", 5)
	if res != nil {
		t.Errorf("expected nil results for empty store, got %v", res)
	}
}

func TestVectorIndexNewEmbDefault(t *testing.T) {
	vi := newAtomVectorIndex(t.TempDir(), nil, func() ([]MemoryAtom, error) { return nil, nil })
	if vi.newEmb == nil {
		t.Error("expected newEmb to be set to default")
	}
	emb := vi.newEmb()
	if emb == nil {
		t.Error("expected non-nil default embedder")
	}
}

func TestVectorIndexCosine(t *testing.T) {
	v1 := vector.Vector{1, 0, 0}
	v2 := vector.Vector{0, 1, 0}
	score := cosine(v1, v2)
	if score != 0 {
		t.Errorf("expected orthogonal vectors to have cosine 0, got %f", score)
	}
}

// TestVectorIndexReusesEmbedderAcrossRebuilds verifies that index rebuilds
// reuse a single embedder instance (preserving backend caches) instead of
// constructing a fresh one per rebuild.
func TestVectorIndexReusesEmbedderAcrossRebuilds(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	factoryCalls := 0
	newEmb := func() embedding.TextEmbedder {
		factoryCalls++
		return newMockEmbedder(vectorDim)
	}
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()
	first := vi.emb

	_ = store.Add(MemoryAtom{ID: "b1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "world", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()

	if vi.emb != first {
		t.Error("expected the embedder instance to be reused across rebuilds")
	}
	if factoryCalls != 1 {
		t.Errorf("expected embedder factory to be called once, got %d", factoryCalls)
	}
	if !vi.ready {
		t.Error("expected index ready after second rebuild")
	}
}

func TestCorpusFingerprint(t *testing.T) {
	atom := func(id string) MemoryAtom { return MemoryAtom{ID: id, Text: "x"} }
	a1 := atom("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")
	a2 := atom("b1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6")

	cases := []struct {
		name  string
		atoms []MemoryAtom
		want  string // exact match only checked against itself; compared below
	}{
		{"empty", nil, ""},
		{"one", []MemoryAtom{a1}, ""},
		{"two", []MemoryAtom{a1, a2}, ""},
	}
	fps := make([]string, len(cases))
	for i, tc := range cases {
		fps[i] = corpusFingerprint(tc.atoms)
		if fps[i] == "" {
			t.Errorf("%s: expected non-empty fingerprint", tc.name)
		}
		if fps[i] != corpusFingerprint(tc.atoms) {
			t.Errorf("%s: fingerprint not deterministic", tc.name)
		}
	}
	for i := 0; i < len(fps); i++ {
		for j := i + 1; j < len(fps); j++ {
			if fps[i] == fps[j] {
				t.Errorf("fingerprints for %q and %q must differ", cases[i].name, cases[j].name)
			}
		}
	}
	// Order of atoms must not matter: IDs are sorted before hashing.
	if corpusFingerprint([]MemoryAtom{a1, a2}) != corpusFingerprint([]MemoryAtom{a2, a1}) {
		t.Error("fingerprint must be order-independent")
	}
}

// TestVectorIndexStaleCorpusDetectedOnLoad verifies that a persisted index
// whose corpus fingerprint no longer matches the atom store is treated as
// dirty and rebuilt instead of silently serving a stale corpus.
func TestVectorIndexStaleCorpusDetectedOnLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello world", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()

	// The persisted meta must carry the corpus fingerprint.
	data, err := os.ReadFile(filepath.Join(dir, vectorMetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta vectorMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	atoms, _ := store.List()
	if meta.Corpus == "" || meta.Corpus != corpusFingerprint(atoms) {
		t.Errorf("expected persisted corpus fingerprint %q, got %q", corpusFingerprint(atoms), meta.Corpus)
	}

	// An atom added after the last persist (process exited before rebuild)
	// must make the persisted index stale: the next process rebuilds.
	_ = store.Add(MemoryAtom{ID: "b1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "fresh atom", SourceClass: SourceUserSaid}, 300)
	vi2 := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi2.emb = newMockEmbedder(vectorDim)
	vi2.ensureFresh()
	if !vi2.ready {
		t.Fatal("expected index to rebuild from stale persisted state")
	}
	vi2.mu.RLock()
	vectors := vi2.store.Len()
	vi2.mu.RUnlock()
	if vectors != 2 {
		t.Errorf("expected rebuilt index to hold 2 vectors, got %d", vectors)
	}
	res := vi2.searchCurrent("fresh atom", 1)
	if len(res) != 1 || res[0].ID != "b1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6" {
		t.Errorf("expected rebuilt index to find the post-persist atom, got %v", res)
	}
}

// TestVectorIndexLegacyMetaWithoutCorpusRebuilds verifies that a meta file
// written before corpus fingerprints were tracked (no corpus field) is
// treated as dirty and rebuilt once, then re-persisted with a fingerprint.
func TestVectorIndexLegacyMetaWithoutCorpusRebuilds(t *testing.T) {
	dir := t.TempDir()
	store := NewAtomStore(dir)
	newEmb := func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	vi := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi.emb = newMockEmbedder(vectorDim)

	_ = store.Add(MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "hello world", SourceClass: SourceUserSaid}, 300)
	vi.markDirty()
	vi.ensureFresh()

	// Simulate a legacy meta file: embedder fingerprint only, no corpus.
	legacy, _ := json.Marshal(vectorMeta{Fingerprint: newMockEmbedder(vectorDim).Fingerprint()})
	if err := os.WriteFile(filepath.Join(dir, vectorMetaFile), legacy, 0600); err != nil {
		t.Fatal(err)
	}

	vi2 := newAtomVectorIndex(dir, newEmb, func() ([]MemoryAtom, error) { return store.List() })
	vi2.emb = newMockEmbedder(vectorDim)
	vi2.ensureFresh()
	if !vi2.ready {
		t.Fatal("expected index to rebuild for legacy meta without corpus fingerprint")
	}
	data, err := os.ReadFile(filepath.Join(dir, vectorMetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta vectorMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	atoms, _ := store.List()
	if meta.Corpus != corpusFingerprint(atoms) {
		t.Errorf("expected re-persisted corpus fingerprint %q, got %q", corpusFingerprint(atoms), meta.Corpus)
	}
}
