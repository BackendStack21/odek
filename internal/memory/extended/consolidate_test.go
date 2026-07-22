package extended

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
	"github.com/BackendStack21/odek/internal/guard"
)

// newConsolidationEM builds an enabled ExtendedMemory with the mock embedder
// and semantic dedup disabled so near-duplicate atoms stay separate until
// consolidation merges them.
func newConsolidationEM(t *testing.T, llm LLMClient) *ExtendedMemory {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticDedupThreshold = floatPtr(0)
	em := New(dir, llm, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	t.Cleanup(func() { em.Close() })
	return em
}

// TestConsolidateAtomsMergesNearDuplicates verifies that a group of
// near-duplicate live atoms is merged into a single atom via one LLM call:
// the originals are removed, the highest confidence is kept, CreatedAt is
// refreshed, and unrelated atoms are untouched.
func TestConsolidateAtomsMergesNearDuplicates(t *testing.T) {
	llm := newMockLLM("User prefers Go for backend services and tools")
	em := newConsolidationEM(t, llm)

	old := MemoryAtom{
		Text:        "User prefers Go for backend services",
		SourceClass: SourceUserSaid,
		Confidence:  0.5,
		CreatedAt:   time.Now().UTC().Add(-48 * time.Hour),
	}
	newer := MemoryAtom{Text: "User prefers Go for backend services and tools", SourceClass: SourceUserSaid, Confidence: 0.9}
	distinct := MemoryAtom{Text: "zzz qqq xxx vvv", SourceClass: SourceUserSaid}
	for _, a := range []MemoryAtom{old, newer, distinct} {
		if err := em.AddAtom(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}

	merged, err := em.ConsolidateAtoms(context.Background())
	if err != nil {
		t.Fatalf("ConsolidateAtoms failed: %v", err)
	}
	if merged != 1 {
		t.Errorf("expected 1 merged group, got %d", merged)
	}
	if got := llm.calls(); got != 1 {
		t.Errorf("expected a single LLM call for the group, got %d", got)
	}
	atoms, _ := em.List()
	if len(atoms) != 2 {
		t.Fatalf("expected 2 atoms after consolidation, got %d", len(atoms))
	}
	var mergedAtom *MemoryAtom
	for i := range atoms {
		if atoms[i].Text == "User prefers Go for backend services and tools" {
			mergedAtom = &atoms[i]
		}
	}
	if mergedAtom == nil {
		t.Fatalf("expected merged atom text %q in %+v", "User prefers Go for backend services and tools", atoms)
	}
	if mergedAtom.Confidence != 0.9 {
		t.Errorf("expected the group's highest confidence 0.9, got %f", mergedAtom.Confidence)
	}
	if time.Since(mergedAtom.CreatedAt) > time.Minute {
		t.Error("expected refreshed CreatedAt on the merged atom")
	}
}

// TestConsolidateAtomsKeepsOriginalsOnFailures verifies that the originals
// survive any per-group failure: LLM error, empty response, and scan
// rejection of the merged text.
func TestConsolidateAtomsKeepsOriginalsOnFailures(t *testing.T) {
	addPair := func(t *testing.T, em *ExtendedMemory) {
		t.Helper()
		for _, a := range []MemoryAtom{
			{Text: "User prefers Go for backend services", SourceClass: SourceUserSaid},
			{Text: "User prefers Go for backend services and tools", SourceClass: SourceUserSaid},
		} {
			if err := em.AddAtom(context.Background(), a); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("llm error", func(t *testing.T) {
		em := newConsolidationEM(t, &mockLLMWithError{err: errors.New("llm fail")})
		addPair(t, em)
		merged, err := em.ConsolidateAtoms(context.Background())
		if err != nil {
			t.Fatalf("ConsolidateAtoms failed: %v", err)
		}
		if merged != 0 {
			t.Errorf("expected 0 merges on LLM error, got %d", merged)
		}
		if atoms, _ := em.List(); len(atoms) != 2 {
			t.Errorf("expected originals kept on LLM error, got %d atoms", len(atoms))
		}
	})

	t.Run("empty response", func(t *testing.T) {
		em := newConsolidationEM(t, newMockLLM("   "))
		addPair(t, em)
		merged, err := em.ConsolidateAtoms(context.Background())
		if err != nil {
			t.Fatalf("ConsolidateAtoms failed: %v", err)
		}
		if merged != 0 {
			t.Errorf("expected 0 merges on empty response, got %d", merged)
		}
		if atoms, _ := em.List(); len(atoms) != 2 {
			t.Errorf("expected originals kept on empty response, got %d atoms", len(atoms))
		}
	})

	t.Run("scan rejection", func(t *testing.T) {
		em := newConsolidationEM(t, newMockLLM("User prefers Go for backend services and tools"))
		addPair(t, em)
		em.SetGuard(rejectAllGuard{}, guard.Config{})
		merged, err := em.ConsolidateAtoms(context.Background())
		if err != nil {
			t.Fatalf("ConsolidateAtoms failed: %v", err)
		}
		if merged != 0 {
			t.Errorf("expected 0 merges on scan rejection, got %d", merged)
		}
		if atoms, _ := em.List(); len(atoms) != 2 {
			t.Errorf("expected originals kept on scan rejection, got %d atoms", len(atoms))
		}
	})
}

// TestConsolidateAtomsSkipsQuarantine verifies that quarantined atoms are
// never pulled into consolidation groups.
func TestConsolidateAtomsSkipsQuarantine(t *testing.T) {
	llm := newMockLLM("User prefers Go for backend services and tools")
	em := newConsolidationEM(t, llm)

	for _, a := range []MemoryAtom{
		{Text: "User prefers Go for backend services", SourceClass: SourceUserSaid},
		{Text: "User prefers Go for backend services and tools", SourceClass: SourceUserSaid},
	} {
		if err := em.AddAtom(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	// Tainted near-duplicate: goes to quarantine, not the live store.
	tainted := MemoryAtom{Text: "User prefers Go for backend services and tools indeed", SourceClass: SourceWeb}
	if err := em.AddAtom(context.Background(), tainted); err != nil {
		t.Fatal(err)
	}

	merged, err := em.ConsolidateAtoms(context.Background())
	if err != nil {
		t.Fatalf("ConsolidateAtoms failed: %v", err)
	}
	if merged != 1 {
		t.Errorf("expected 1 merged group (live atoms only), got %d", merged)
	}
	if atoms, _ := em.List(); len(atoms) != 1 {
		t.Errorf("expected 1 live atom after consolidation, got %d", len(atoms))
	}
	if q, _ := em.ListQuarantine(); len(q) != 1 {
		t.Errorf("expected quarantined atom untouched, got %d", len(q))
	}
}

// TestConsolidateAtomsPreconditions verifies the disabled and no-LLM error
// paths.
func TestConsolidateAtomsPreconditions(t *testing.T) {
	var nilEM *ExtendedMemory
	if _, err := nilEM.ConsolidateAtoms(context.Background()); err == nil {
		t.Error("expected error on nil ExtendedMemory")
	}

	disabled := New(t.TempDir(), newMockLLM(), DefaultConfig())
	if _, err := disabled.ConsolidateAtoms(context.Background()); err == nil {
		t.Error("expected error when disabled")
	}

	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	noLLM := New(t.TempDir(), nil, cfg)
	if _, err := noLLM.ConsolidateAtoms(context.Background()); err == nil {
		t.Error("expected error without an LLM")
	}
}

// TestGroupBySimilarity verifies the greedy near-duplicate clustering.
func TestGroupBySimilarity(t *testing.T) {
	atom := func(id string) MemoryAtom { return MemoryAtom{ID: id, Text: id} }
	a, b, c, d := atom("a"), atom("b"), atom("c"), atom("d")

	cases := []struct {
		name      string
		atoms     []MemoryAtom
		vecs      []vector.Vector
		threshold float32
		want      [][]string // expected groups as atom IDs
	}{
		{
			name:      "one pair grouped",
			atoms:     []MemoryAtom{a, b, c},
			vecs:      []vector.Vector{{1, 0}, {1, 0}, {0, 1}},
			threshold: 0.9,
			want:      [][]string{{"a", "b"}},
		},
		{
			name:      "below threshold stays separate",
			atoms:     []MemoryAtom{a, b},
			vecs:      []vector.Vector{{1, 0}, {0.7, 0.7}},
			threshold: 0.9,
			want:      nil,
		},
		{
			name:      "nil vectors skipped",
			atoms:     []MemoryAtom{a, b, c, d},
			vecs:      []vector.Vector{nil, {1, 0}, {1, 0}, nil},
			threshold: 0.9,
			want:      [][]string{{"b", "c"}},
		},
		{
			name:      "chain groups with first member only",
			atoms:     []MemoryAtom{a, b, c},
			vecs:      []vector.Vector{{1, 0}, {1, 0.09}, {0, 1}},
			threshold: 0.9,
			want:      [][]string{{"a", "b"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := groupBySimilarity(tc.atoms, tc.vecs, tc.threshold)
			if len(groups) != len(tc.want) {
				t.Fatalf("expected %d groups, got %d (%v)", len(tc.want), len(groups), groups)
			}
			for i, g := range groups {
				if len(g) != len(tc.want[i]) {
					t.Fatalf("group %d: expected %d members, got %d", i, len(tc.want[i]), len(g))
				}
				for j, m := range g {
					if m.ID != tc.want[i][j] {
						t.Errorf("group %d member %d: expected %q, got %q", i, j, tc.want[i][j], m.ID)
					}
				}
			}
		})
	}
}
