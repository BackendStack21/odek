package extended

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
)

// memoryFixRankLLM answers a rerank prompt by putting the candidate whose
// text contains prefer first, followed by the rest in prompt order.
type memoryFixRankLLM struct {
	prefer string
	calls  atomic.Int32
}

func (r *memoryFixRankLLM) SimpleCall(_ context.Context, _, user string) (string, error) {
	r.calls.Add(1)
	var first, rest []string
	for _, line := range strings.Split(user, "\n") {
		var idx int
		if _, err := fmt.Sscanf(line, "[%d]", &idx); err != nil {
			continue
		}
		if strings.Contains(line, r.prefer) {
			first = append(first, fmt.Sprint(idx))
		} else {
			rest = append(rest, fmt.Sprint(idx))
		}
	}
	return strings.Join(append(first, rest...), ","), nil
}

// TestRED_RecallRerankOrderDiscarded: queryAtomsScored calls the paid LLM
// rerank, which reorders the slice but leaves every candidate's composite
// score untouched, then immediately sort.SliceStable's the slice by that
// score — restoring the pre-rerank order. The rerank's judgment never
// reaches the caller (and with top-k truncation after the sort, an atom the
// LLM ranked first can be cut entirely), so every recall with
// semantic_search_rerank on pays for an LLM call that has no effect.
func TestRED_RecallRerankOrderDiscarded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchRerank = boolPtr(true)
	cfg.SemanticSearchTopK = 2
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticDedupThreshold = floatPtr(0)

	llm := &memoryFixRankLLM{prefer: "Go for backend"}
	em := New(t.TempDir(), llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()
	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services"))
	_ = em.AddAtom(context.Background(), makeSearchableAtom("User prefers Python for data science"))

	// Without rerank, the literal query ranks Python first.
	em.recall.cfg.SemanticSearchRerank = boolPtr(false)
	base, err := em.recall.queryAtoms(context.Background(), "Python data science")
	if err != nil || len(base) != 2 || !strings.Contains(base[0].Text, "Python") {
		t.Fatalf("setup: unreranked order = %v (err %v), want Python first", base, err)
	}

	em.recall.cfg.SemanticSearchRerank = boolPtr(true)
	llm.calls.Store(0)
	atoms, err := em.recall.queryAtoms(context.Background(), "Python data science")
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls.Load() == 0 {
		t.Fatal("setup: rerank LLM was not called")
	}
	if len(atoms) != 2 || !strings.Contains(atoms[0].Text, "Go for backend") {
		texts := make([]string, len(atoms))
		for i, a := range atoms {
			texts[i] = a.Text
		}
		t.Fatalf("LLM reranked Go first, but recall returned %q", texts)
	}

	// The turn-start recall path re-sorts its union by score; the rerank
	// order must survive that too.
	union, err := em.recall.queryAtomsWithPrediction(context.Background(), "Python data science", nil, UserState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(union) == 0 || !strings.Contains(union[0].Text, "Go for backend") {
		t.Fatalf("recall union lost the rerank order: %v", union)
	}
}

// TestRED_AtomIndexRebuildRefitsSharedEmbedderUnderReaders: ensureFresh
// runs the rebuild's emb.Fit/EmbedAll OFF the index lock on the SAME
// embedder instance (vi.emb) that searchCurrent, similarity and embedTexts
// keep using concurrently (searchCurrent under only the read lock, the
// other two with no lock at all). The default RandomProjections embedder
// has no internal locking, so a recall or semantic-dedup lookup that
// overlaps a rebuild is a data race on the embedder's vocabulary and
// projection tables — and, even without the race detector, embeds queries
// against a half-fitted vocabulary while the store still holds vectors from
// the previous fit. Run with -race.
func TestRED_AtomIndexRebuildRefitsSharedEmbedderUnderReaders(t *testing.T) {
	atoms := make([]MemoryAtom, 200)
	for i := range atoms {
		atoms[i] = MemoryAtom{ID: fmt.Sprintf("%032x", i+1), Text: fmt.Sprintf("user fact number %d about topic%d and subject%d", i, i%17, i%23)}
	}
	dir := t.TempDir()
	vi := newAtomVectorIndex(dir, func() textEmbedder { return embedding.NewRP(vectorDim) },
		func() ([]MemoryAtom, error) { return atoms, nil })
	vi.ensureFresh() // initial build: index ready
	vi.markDirty()

	done := make(chan struct{})
	go func() {
		defer close(done)
		vi.ensureFresh() // rebuild
	}()
	for i := 0; i < 200; i++ {
		_ = vi.searchCurrent("topic3 subject5", 3)
		_ = vi.similarity("user fact topic1", "subject2 fact")
		_, _ = vi.embedTexts([]string{"topic4 subject6"})
	}
	<-done
	if got := vi.searchCurrent("topic3 subject5", 3); len(got) == 0 {
		t.Fatal("rebuilt index returned no results")
	}

	// A cold start that loads persisted state must not mutate an instance
	// readers may already hold either.
	cold := newAtomVectorIndex(dir, func() textEmbedder { return embedding.NewRP(vectorDim) },
		func() ([]MemoryAtom, error) { return atoms, nil })
	done = make(chan struct{})
	go func() {
		defer close(done)
		cold.ensureFresh() // tryLoadLocked
	}()
	for i := 0; i < 50; i++ {
		_ = cold.similarity("user fact topic1", "subject2 fact")
	}
	<-done
	if got := cold.searchCurrent("topic3 subject5", 3); len(got) == 0 {
		t.Fatal("loaded index returned no results")
	}
}

// memoryFixSharedEmb is a concurrency-safe embedder that declares it may be
// refitted while shared.
type memoryFixSharedEmb struct{ *mockEmbedder }

func (memoryFixSharedEmb) SharedRefitSafe() bool { return true }

// TestRED_AtomIndexKeepsRefitSafeEmbedder: an embedder that is safe to refit
// while shared (the HTTP backend, whose per-instance cache keeps re-fits
// cheap) keeps its live instance across rebuilds; others get a fresh one.
func TestRED_AtomIndexKeepsRefitSafeEmbedder(t *testing.T) {
	atoms := []MemoryAtom{{ID: fmt.Sprintf("%032x", 1), Text: "user likes tea"}}
	list := func() ([]MemoryAtom, error) { return atoms, nil }

	shared := memoryFixSharedEmb{newMockEmbedder(vectorDim)}
	vi := newAtomVectorIndex(t.TempDir(), func() textEmbedder { return shared }, list)
	vi.ensureFresh()
	vi.markDirty()
	vi.ensureFresh()
	if vi.emb != textEmbedder(shared) {
		t.Fatal("refit-safe embedder was replaced on rebuild")
	}

	vi2 := newAtomVectorIndex(t.TempDir(), func() textEmbedder { return embedding.NewRP(vectorDim) }, list)
	vi2.ensureFresh()
	first := vi2.emb
	vi2.markDirty()
	vi2.ensureFresh()
	if vi2.emb == first {
		t.Fatal("RandomProjections embedder was refitted in place")
	}

	httpEmb := embedding.New(&embedding.Config{Provider: "http", BaseURL: "http://127.0.0.1:1/v1", Model: "m"}, vectorDim)
	if s, ok := httpEmb.(sharedRefitSafe); !ok || !s.SharedRefitSafe() {
		t.Fatal("HTTP embedder does not declare it is safe to refit while shared")
	}
}
