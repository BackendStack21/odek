package extended

import (
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
