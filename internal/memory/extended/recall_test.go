package extended

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// The mock embedder rates these two English sentences as near-duplicates;
	// disable semantic dedup so both atoms reach the rerank path.
	cfg.SemanticDedupThreshold = floatPtr(0)

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
	// The mock embedder rates these two English sentences as near-duplicates;
	// disable semantic dedup so both atoms reach the rerank path.
	cfg.SemanticDedupThreshold = floatPtr(0)

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

// TestPredictiveRecallSkipsRerank verifies that predicted-intent searches
// reuse the first query's candidate set and do not trigger additional paid
// LLM reranks — only the literal query is reranked.
func TestPredictiveRecallSkipsRerank(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchRerank = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.PredictiveIntents = 1
	cfg.FollowUpAnticipationEnabled = boolPtr(true)

	llm := newMockLLM("0,1", `[{"text":"follow-up question","confidence":0.9}]`)
	em := New(dir, llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services"))
	_ = em.AddAtom(context.Background(), makeSearchableAtom("Run go test ./... to verify"))

	if _, err := em.recall.queryAtomsWithPrediction(context.Background(), "Go backend", nil, UserState{}); err != nil {
		t.Fatalf("queryAtomsWithPrediction failed: %v", err)
	}
	// 1 rerank for the literal query + 1 prediction call. The predicted-intent
	// searches must not trigger additional reranks (old behavior: 4 calls).
	if got := llm.calls(); got != 2 {
		t.Errorf("expected 2 LLM calls (literal rerank + prediction), got %d", got)
	}
}

// TestQueryAtomsWithPredictionCompositeOrdering verifies the final union is
// sorted by the blended composite score (0.6*similarity + 0.4*retention), not
// by pure retention: a highly similar but decayed atom must outrank a fresh
// but barely similar one.
func TestQueryAtomsWithPredictionCompositeOrdering(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)
	cfg.FollowUpAnticipationEnabled = boolPtr(false)

	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	query := "zzzz qqqq wwww"
	// Identical to the query (similarity 1.0) but almost fully decayed.
	decayed := makeSearchableAtom(query)
	decayed.CreatedAt = time.Now().UTC().Add(-300 * 24 * time.Hour)
	// Barely similar but brand new: pure retention ranks this one first.
	fresh := makeSearchableAtom("completely unrelated memory entry")
	if err := em.AddAtom(context.Background(), decayed); err != nil {
		t.Fatal(err)
	}
	if err := em.AddAtom(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}

	atoms, err := em.recall.queryAtomsWithPrediction(context.Background(), query, nil, UserState{})
	if err != nil {
		t.Fatalf("queryAtomsWithPrediction failed: %v", err)
	}
	if len(atoms) != 2 {
		t.Fatalf("expected 2 atoms, got %d", len(atoms))
	}
	if atoms[0].Text != query {
		t.Errorf("expected composite score to rank the similar atom first, got %q", atoms[0].Text)
	}
}

// newIntentWeightedRecallEM builds an enabled ExtendedMemory whose recall
// uses the mock embedder, no rerank, and prediction enabled. The corpus is
// two atoms with (almost) disjoint character sets so the literal query
// matches only literalAtom and the predicted intent matches only intentAtom.
func newIntentWeightedRecallEM(t *testing.T, intentJSON string) *ExtendedMemory {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.5
	cfg.SemanticSearchRerank = boolPtr(false)
	cfg.PredictiveIntents = 1
	cfg.FollowUpAnticipationEnabled = boolPtr(true)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	em.recall.predictor = NewPredictor(newMockLLM(intentJSON), cfg)
	t.Cleanup(func() { em.Close() })

	// Literal match: composite 0.6*~0.92 + 0.4*1.0 ≈ 0.95.
	if err := em.AddAtom(context.Background(), makeSearchableAtom("mmm nnn ooo ppp qqq")); err != nil {
		t.Fatal(err)
	}
	// Predicted-intent match: similarity 1.0 to the intent text, composite
	// 1.0 before confidence weighting.
	if err := em.AddAtom(context.Background(), makeSearchableAtom("xxx yyy zzz")); err != nil {
		t.Fatal(err)
	}
	return em
}

// TestPredictedIntentConfidenceWeightsScore verifies that atoms found via a
// predicted intent have their composite score multiplied by the intent's
// confidence: a high-confidence intent can outrank the literal match, a
// mediocre one cannot.
func TestPredictedIntentConfidenceWeightsScore(t *testing.T) {
	cases := []struct {
		name       string
		confidence float32
		wantFirst  string
	}{
		{"high confidence outranks literal", 0.99, "xxx yyy zzz"},
		{"mediocre confidence ranks below literal", 0.5, "mmm nnn ooo ppp qqq"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			em := newIntentWeightedRecallEM(t, `[{"text":"xxx yyy zzz","confidence":`+fmt.Sprintf("%v", tc.confidence)+`}]`)
			atoms, err := em.recall.queryAtomsWithPrediction(context.Background(), "mmm nnn ooo ppp", nil, UserState{})
			if err != nil {
				t.Fatalf("queryAtomsWithPrediction failed: %v", err)
			}
			if len(atoms) != 2 {
				t.Fatalf("expected 2 atoms, got %d", len(atoms))
			}
			if atoms[0].Text != tc.wantFirst {
				t.Errorf("confidence %v: expected %q first, got %q", tc.confidence, tc.wantFirst, atoms[0].Text)
			}
		})
	}
}

// TestPredictedIntentLowConfidenceSkipped verifies that predicted intents
// below minPredictedIntentConfidence are not searched at all.
func TestPredictedIntentLowConfidenceSkipped(t *testing.T) {
	em := newIntentWeightedRecallEM(t, `[{"text":"xxx yyy zzz","confidence":0.2}]`)
	atoms, err := em.recall.queryAtomsWithPrediction(context.Background(), "mmm nnn ooo ppp", nil, UserState{})
	if err != nil {
		t.Fatalf("queryAtomsWithPrediction failed: %v", err)
	}
	if len(atoms) != 1 || atoms[0].Text != "mmm nnn ooo ppp qqq" {
		t.Errorf("expected only the literal match for a low-confidence intent, got %+v", atoms)
	}
}
