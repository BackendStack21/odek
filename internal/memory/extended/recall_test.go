package extended

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
)

func makeSearchableAtom(text string) MemoryAtom {
	return MemoryAtom{Text: text, SourceClass: SourceUserApproved, Type: TypeFact}
}

func TestRecallBasic(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services"))

	atoms, err := em.recall.queryAtoms(context.Background(), "Go backend")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) == 0 {
		t.Fatal("expected at least one search result")
	}
}

func TestRecallRerank(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchRerank = boolPtr(true)
	cfg.SemanticSearchTopK = 2
	cfg.SemanticSearchMinScore = 0.01

	llm := newMockLLM("1,0") // reorder: second atom first
	em := New(dir, llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)

	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services"))
	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Python for data science"))

	atoms, err := em.recall.queryAtoms(context.Background(), "Python data science")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) != 2 {
		t.Fatalf("expected 2 atoms, got %d", len(atoms))
	}
	if !strings.Contains(atoms[0].Text, "Python") {
		t.Errorf("expected Python first after rerank, got %q", atoms[0].Text)
	}
}

func TestRecallMinScoreFiltersResults(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.95 // very high, should exclude everything
	cfg.SemanticSearchRerank = boolPtr(false)

	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Project uses Postgres database"))

	atoms, err := em.recall.queryAtoms(context.Background(), "Postgres database")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) != 0 {
		t.Errorf("expected no atoms with high min_score, got %d", len(atoms))
	}
}

func TestRecallOverfetch(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchTopK = 1
	cfg.SemanticSearchOverfetch = 3
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)

	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Alpha"))
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Beta"))
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Gamma"))

	atoms, err := em.recall.queryAtoms(context.Background(), "Beta")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) != 1 {
		t.Errorf("expected overfetch to be truncated to topK=1, got %d", len(atoms))
	}
}

func TestRecallBudgetTruncation(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.MemoryBudgetChars = 50
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)

	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Very long atom text that exceeds the budget"))

	ctx := em.FormatContext(context.Background(), "long")
	if len(ctx) > cfg.MemoryBudgetChars+40 {
		t.Errorf("context length %d exceeds budget %d", len(ctx), cfg.MemoryBudgetChars)
	}
}

func TestRecallExcludesTainted(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)

	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), MemoryAtom{Text: "tainted data", SourceClass: SourceWeb})
	_ = em.AddAtom(context.Background(), makeSearchableAtom("trusted data"))

	atoms, err := em.recall.queryAtoms(context.Background(), "data")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	for _, a := range atoms {
		if IsTaintedSourceClass(a.SourceClass) {
			t.Errorf("recall returned tainted atom: %+v", a)
		}
	}
}

func TestRecallRerankErrorFallsBack(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchRerank = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01

	llm := newMockLLM() // returns empty
	em := New(dir, llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	if err := em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services")); err != nil {
		t.Fatalf("AddAtom 1: %v", err)
	}
	if err := em.AddAtom(context.Background(), makeSearchableAtom("User prefers Python for data science")); err != nil {
		t.Fatalf("AddAtom 2: %v", err)
	}

	atoms, err := em.recall.queryAtoms(context.Background(), "Python data")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) != 2 {
		t.Errorf("expected fallback to return both atoms, got %d", len(atoms))
	}
}

func TestRecallQueryWithNilStore(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(t.TempDir(), newMockLLM(), cfg)
	em.recall.store = nil
	em.recall.index = nil

	ctx := em.FormatContext(context.Background(), "hello")
	if ctx != "" {
		t.Errorf("expected empty context when store/index nil, got %q", ctx)
	}
}

func TestRecallQueryReturnsContext(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Project uses Postgres"))

	ctx := em.FormatContext(context.Background(), "Postgres")
	if ctx == "" {
		t.Error("expected non-empty context for matching atom")
	}
}

func TestEmbedderRanker(t *testing.T) {
	cfg := DefaultConfig()
	ranker := embedderRanker(cfg)
	atoms := []MemoryAtom{
		{Text: "alpha beta gamma", Type: TypeFact},
		{Text: "beta gamma delta", Type: TypeFact},
		{Text: "zeta eta theta", Type: TypeFact},
	}
	ranked, err := ranker("beta gamma", atoms)
	if err != nil {
		t.Fatalf("ranker failed: %v", err)
	}
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked atoms, got %d", len(ranked))
	}
	// The first two atoms should be ranked above the unrelated third.
	if ranked[0].Text != "alpha beta gamma" && ranked[0].Text != "beta gamma delta" {
		t.Errorf("expected top atom to be related to query, got %q", ranked[0].Text)
	}
}

func TestRecallRerankIgnoresInvalidIndices(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchRerank = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01

	llm := newMockLLM("99,abc,0")
	em := New(dir, llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	_ = em.AddAtom(context.Background(), makeSearchableAtom("A"))

	atoms, err := em.recall.queryAtoms(context.Background(), "A")
	if err != nil {
		t.Fatalf("queryAtoms failed: %v", err)
	}
	if len(atoms) != 1 {
		t.Errorf("expected 1 atom after filtering invalid indices, got %d", len(atoms))
	}
}
