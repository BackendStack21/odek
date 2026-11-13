package extended

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// A new atom links to the same-session atoms of the nearest turns first and,
// within a turn distance, to the newest ones, at most maxTemporalLinksPerAtom.
func TestTemporalLinksPreferNearestTurnsThenNewest(t *testing.T) {
	em := rh1112MemOpen(t, t.TempDir(), func(c *Config) {
		c.SemanticDedupThreshold = floatPtr(0)
		c.AssociationSemanticTopK = 0
	})
	add := func(text string, turn int) string {
		t.Helper()
		if err := em.AddAtom(context.Background(), MemoryAtom{
			Text: text, SourceClass: SourceUserSaid, Type: TypeFact,
			Context: AtomContext{SessionID: "s-temporal", Turn: turn},
		}); err != nil {
			t.Fatal(err)
		}
		atoms, _ := em.store.List()
		for _, a := range atoms {
			if a.Text == text {
				return a.ID
			}
		}
		t.Fatalf("atom %q not stored", text)
		return ""
	}
	var far, near []string
	for i := 0; i < 6; i++ {
		far = append(far, add(fmt.Sprintf("far turn note %d about item %x", i, i*31), 1))
		time.Sleep(2 * time.Millisecond) // distinct creation times
	}
	for i := 0; i < 6; i++ {
		near = append(near, add(fmt.Sprintf("near turn note %d about item %x", i, i*37), 3))
		time.Sleep(2 * time.Millisecond)
	}
	id := add("the newest note in turn three", 3)
	got := em.assoc.Related(id)
	if len(got) != maxTemporalLinksPerAtom {
		t.Fatalf("linked %d atoms, want %d: %v", len(got), maxTemporalLinksPerAtom, got)
	}
	for _, n := range near {
		if !slices.Contains(got, n) {
			t.Fatalf("a same-turn atom %s was skipped for a farther one: %v", n, got)
		}
	}
	// The two remaining slots go to the newest atoms two turns away.
	if !slices.Contains(got, far[5]) || !slices.Contains(got, far[4]) || slices.Contains(got, far[0]) {
		t.Fatalf("turn-1 links are not the newest: %v (turn-1 ids %v)", got, far)
	}
}

// A refreshed duplicate keeps its old id, which may already hold many links;
// its atoms.json record still lists at most maxLinksPerAtom related atoms,
// the newest ones.
func TestRelatedRecordBoundedForAHub(t *testing.T) {
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, 60)
	em := rh1112MemOpen(t, dir, func(c *Config) { c.SemanticDedupThreshold = floatPtr(0) })
	atoms, _ := em.store.List() // newest first
	hub := atoms[len(atoms)-1]
	for _, a := range atoms[:len(atoms)-1] {
		em.assoc.Link(hub.ID, a.ID)
	}
	if err := em.AddAtom(context.Background(), MemoryAtom{
		Text: hub.Text, SourceClass: SourceUserSaid, Type: hub.Type,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := em.store.Get(hub.ID)
	if err != nil {
		t.Fatal(err)
	}
	related := got.Context.RelatedAtomIDs
	if len(related) != maxLinksPerAtom {
		t.Fatalf("record lists %d related atoms, want %d", len(related), maxLinksPerAtom)
	}
	if !slices.IsSorted(related) {
		t.Fatalf("record related list is not sorted: %v", related)
	}
	// The refreshed hub is now the newest atom; its record holds the newest
	// of the rest.
	for _, a := range atoms[:maxLinksPerAtom] {
		if a.ID != hub.ID && !slices.Contains(related, a.ID) {
			t.Fatalf("newest neighbour %s missing from the record", a.ID)
		}
	}
}

// Without the atom list (a failed listing) the record is still bounded.
func TestRelatedForRecordWithoutAtomList(t *testing.T) {
	em := rh1112MemOpen(t, t.TempDir(), nil)
	hub := fmt.Sprintf("%032x", 1)
	for i := 2; i < 50; i++ {
		em.assoc.Link(hub, fmt.Sprintf("%032x", i))
	}
	if got := em.relatedForRecord(hub, nil); len(got) != maxLinksPerAtom {
		t.Fatalf("got %d related ids, want %d", len(got), maxLinksPerAtom)
	}
}

// A consolidated atom's record keeps at most maxLinksPerAtom of the group's
// related atoms, also when associations are disabled and the add path keeps
// the record as built.
func TestConsolidatedRecordRelatedBounded(t *testing.T) {
	llm := newMockLLM("User prefers Go for backend services and command line tools")
	em := newConsolidationEM(t, llm)
	em.cfg.AssociationsEnabled = boolPtr(false)
	group := []MemoryAtom{
		{ID: fmt.Sprintf("%032x", 0xa1), Text: "User prefers Go for backend services", SourceClass: SourceUserApproved, Type: TypePreference, Confidence: 0.9},
		{ID: fmt.Sprintf("%032x", 0xa2), Text: "User prefers Go for backend tools", SourceClass: SourceUserApproved, Type: TypePreference, Confidence: 0.8},
	}
	for g := range group {
		for i := 0; i < 20; i++ {
			group[g].Context.RelatedAtomIDs = append(group[g].Context.RelatedAtomIDs, fmt.Sprintf("%032x", 0x1000+g*100+i))
		}
		if err := em.store.Add(group[g], em.cfg.AtomMaxChars); err != nil {
			t.Fatal(err)
		}
	}
	if !em.mergeGroup(context.Background(), group) {
		t.Fatal("group not merged")
	}
	atoms, _ := em.store.List()
	if len(atoms) != 1 {
		t.Fatalf("got %d atoms after the merge, want 1", len(atoms))
	}
	if n := len(atoms[0].Context.RelatedAtomIDs); n != maxLinksPerAtom {
		t.Fatalf("merged record lists %d related atoms, want %d", n, maxLinksPerAtom)
	}
}
