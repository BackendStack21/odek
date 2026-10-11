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
