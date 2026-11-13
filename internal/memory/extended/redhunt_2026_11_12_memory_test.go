package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/embedding"
)

// rh1112MemSeedStore writes n trusted atoms straight to disk (chunks plus one
// atoms.json), the layout AtomStore.Add produces, without paying n
// read-modify-write cycles in the test setup.
func rh1112MemSeedStore(t *testing.T, dir string, n int) {
	t.Helper()
	chunks := filepath.Join(dir, "chunks")
	if err := os.MkdirAll(chunks, 0o700); err != nil {
		t.Fatal(err)
	}
	metas := make([]atomMeta, 0, n)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%032x", i+1)
		text := fmt.Sprintf("the user prefers tool number %d for task kind %d", i, i%7)
		if err := os.WriteFile(filepath.Join(chunks, id+".md"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, atomMeta{
			ID: id, SourceClass: SourceUserSaid, Type: TypeFact, Confidence: 0.7,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			Context:   AtomContext{SessionID: "20260101-seeded", Project: "seeded", Turn: i / 8},
		})
	}
	data, err := json.MarshalIndent(metas, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "atoms.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// rh1112MemOpen opens an enabled ExtendedMemory over dir with the
// deterministic mock embedder and no memory LLM.
func rh1112MemOpen(t *testing.T, dir string, mutate func(*Config)) *ExtendedMemory {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	if mutate != nil {
		mutate(&cfg)
	}
	em := New(dir, nil, cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	t.Cleanup(func() { _ = em.Close() })
	return em
}

// rh1112MemParsesForOneAdd returns how many times one AddAtom parses
// atoms.json on a store that already holds n atoms.
func rh1112MemParsesForOneAdd(t *testing.T, n int) int64 {
	t.Helper()
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, n)
	em := rh1112MemOpen(t, dir, nil)
	before := em.store.atomsParses.Load()
	err := em.AddAtom(context.Background(), MemoryAtom{
		Text: "the user likes tabs over spaces", SourceClass: SourceUserSaid, Type: TypePreference,
	})
	if err != nil {
		t.Fatalf("AddAtom with %d stored atoms: %v", n, err)
	}
	return em.store.atomsParses.Load() - before
}

// TestRED_AddAtomCostDoesNotGrowWithStoreSize: adding one atom must read the
// atoms.json metadata a bounded number of times, whatever the store size.
//
// enforceCap runs on every add (before it even compares the total with the
// cap) and sizes the store through buildSizedAtoms, which calls
// AtomStore.AtomSize once per stored atom; AtomSize re-reads and re-parses the
// whole atoms.json (uncached loadAtomsLocked) on every call. One add therefore
// parses atoms.json N times, i.e. O(N^2) bytes, and per-turn extraction does
// it once per extracted atom. Measured: 1 add at 1600 atoms takes 2.4 s; the
// documented capacity is 16,000-18,000 atoms.
func TestRED_AddAtomCostDoesNotGrowWithStoreSize(t *testing.T) {
	small := rh1112MemParsesForOneAdd(t, 100)
	large := rh1112MemParsesForOneAdd(t, 400)
	if large > small+4 {
		t.Fatalf("one AddAtom parsed atoms.json %d times with 100 atoms stored and %d times with 400: "+
			"the work per add grows with the store (enforceCap -> buildSizedAtoms -> AtomSize re-parses atoms.json per atom)",
			small, large)
	}
}

// rh1112MemTaskLinks adds n same-project decision atoms, each from its own
// session (so no temporal links), and returns the total number of stored
// association links and the size of associations.json.
func rh1112MemTaskLinks(t *testing.T, n int) (links int, fileBytes int64) {
	t.Helper()
	dir := t.TempDir()
	em := rh1112MemOpen(t, dir, func(c *Config) {
		// Keep every atom: the mock embedder is a character histogram, so
		// similar sentences would otherwise be semantic-deduplicated.
		c.SemanticDedupThreshold = floatPtr(0)
	})
	for i := 0; i < n; i++ {
		a := MemoryAtom{
			Text:        fmt.Sprintf("decision %d: module %d uses approach %d", i, i*13, i*7),
			SourceClass: SourceUserSaid,
			Type:        TypeDecision,
			Context:     AtomContext{SessionID: fmt.Sprintf("20260101-sess%04d", i), Project: "payments", Turn: 1},
		}
		if err := em.AddAtom(context.Background(), a); err != nil {
			t.Fatalf("AddAtom %d: %v", i, err)
		}
	}
	atoms, err := em.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != n {
		t.Fatalf("fixture: %d live atoms, want %d", len(atoms), n)
	}
	for _, a := range atoms {
		links += len(em.assoc.Related(a.ID))
	}
	if st, err := os.Stat(filepath.Join(dir, "associations.json")); err == nil {
		fileBytes = st.Size()
	}
	return links, fileBytes
}

// TestRED_TaskAssociationsGrowLinearly: doubling the number of atoms must not
// quadruple the association graph.
//
// buildAssociations links every new goal/decision/convention/intent atom to
// EVERY same-project atom of those types, so the graph is complete per
// project: links, associations.json and the RelatedAtomIDs copied into each
// new atom's atoms.json record all grow with the square of the atom count.
// associations.json is not counted by enforceCap or Size(), although
// docs/EXTENDED_MEMORY.md says the cap covers it and budgets it at ~1 MB.
func TestRED_TaskAssociationsGrowLinearly(t *testing.T) {
	// Past the first few atoms the per-atom link bound is reached, so the
	// graph grows by a constant per atom.
	l20, b20 := rh1112MemTaskLinks(t, 40)
	l40, b40 := rh1112MemTaskLinks(t, 80)
	if l20 == 0 {
		t.Fatal("fixture: no association links at all")
	}
	if float64(l40) > 2.5*float64(l20) {
		t.Fatalf("association links grew %.1fx when atoms doubled (40 atoms: %d links, %d B; 80 atoms: %d links, %d B): "+
			"task associations form a complete graph per project",
			float64(l40)/float64(l20), l20, b20, l40, b40)
	}
}
