package extended

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
)

// The mock embedder scores these two texts at ~0.96 cosine similarity: high
// enough for the default semantic dedup threshold (0.92) but distinct after
// normalization, so the exact-match tier does not catch them.
const (
	semanticDupOriginal  = "User prefers Go for backend services"
	semanticDupRestate   = "User prefers Go for backend services and tools"
	semanticDedupFixConf = 0.9
)

// TestAddAtomSemanticDedupRefreshesExisting verifies that a paraphrased
// re-statement refreshes the existing live atom (original ID kept, higher
// confidence wins, CreatedAt refreshed) instead of appending a duplicate.
func TestAddAtomSemanticDedupRefreshesExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	first := MemoryAtom{
		Text:        semanticDupOriginal,
		SourceClass: SourceUserSaid,
		Confidence:  0.5,
		CreatedAt:   time.Now().UTC().Add(-time.Hour),
	}
	if err := em.AddAtom(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	atoms, _ := em.List()
	if len(atoms) != 1 {
		t.Fatalf("expected 1 atom, got %d", len(atoms))
	}
	originalID := atoms[0].ID

	dup := MemoryAtom{Text: semanticDupRestate, SourceClass: SourceUserSaid, Confidence: semanticDedupFixConf}
	if err := em.AddAtom(context.Background(), dup); err != nil {
		t.Fatal(err)
	}
	atoms, _ = em.List()
	if len(atoms) != 1 {
		t.Fatalf("expected semantic dedup to keep 1 atom, got %d", len(atoms))
	}
	if atoms[0].ID != originalID {
		t.Error("semantic dedup must keep the original atom ID")
	}
	if atoms[0].Confidence != semanticDedupFixConf {
		t.Errorf("expected the higher confidence to be kept, got %f", atoms[0].Confidence)
	}
	if time.Since(atoms[0].CreatedAt) > time.Minute {
		t.Error("expected CreatedAt to be refreshed on semantic dedup")
	}
}

// TestAddAtomSemanticDedupDisabled verifies that a zero threshold disables
// the semantic tier: near-duplicate texts are stored as separate atoms.
func TestAddAtomSemanticDedupDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticDedupThreshold = floatPtr(0)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	_ = em.AddAtom(context.Background(), MemoryAtom{Text: semanticDupOriginal, SourceClass: SourceUserSaid})
	_ = em.AddAtom(context.Background(), MemoryAtom{Text: semanticDupRestate, SourceClass: SourceUserSaid})
	atoms, _ := em.List()
	if len(atoms) != 2 {
		t.Errorf("expected 2 atoms with semantic dedup disabled, got %d", len(atoms))
	}
}

// TestAddAtomSemanticDedupBelowThreshold verifies that pairs scoring below
// the configured threshold are not merged.
func TestAddAtomSemanticDedupBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	// The fixture pair scores ~0.96; a 0.999 threshold must reject it.
	cfg.SemanticDedupThreshold = floatPtr(0.999)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	_ = em.AddAtom(context.Background(), MemoryAtom{Text: semanticDupOriginal, SourceClass: SourceUserSaid})
	_ = em.AddAtom(context.Background(), MemoryAtom{Text: semanticDupRestate, SourceClass: SourceUserSaid})
	atoms, _ := em.List()
	if len(atoms) != 2 {
		t.Errorf("expected 2 atoms below the dedup threshold, got %d", len(atoms))
	}
}

// TestAddAtomsSemanticDedupWithinBatch verifies that atoms stored earlier in
// the same batch (not yet covered by the vector index) are deduped against,
// and that the batch still costs exactly one index rebuild.
func TestAddAtomsSemanticDedupWithinBatch(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(dir, newMockLLM(), cfg)
	emb := &fitCountingEmbedder{mockEmbedder: newMockEmbedder(vectorDim)}
	em.SetEmbedder(emb)
	defer em.Close()

	batch := []MemoryAtom{
		{Text: semanticDupOriginal, SourceClass: SourceUserSaid},
		{Text: semanticDupRestate, SourceClass: SourceUserSaid},
	}
	if err := em.AddAtoms(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	atoms, _ := em.List()
	if len(atoms) != 1 {
		t.Errorf("expected in-batch semantic dedup to keep 1 atom, got %d", len(atoms))
	}
	if got := emb.fits(); got != 1 {
		t.Errorf("expected a single index rebuild for the batch, got %d", got)
	}
}
