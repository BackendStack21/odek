package extended

import (
	"context"
	"testing"
)

// TestRED_ConsolidationDropsUserPinOfLowerConfidenceMember: mergeGroup bases
// the merged atom on the highest-confidence member only, so a pin set by the
// user (`memory pin_atom` / `odek memory extended pin`) on any other member of
// the near-duplicate group is silently discarded. The merged atom is then
// evictable and, for ephemeral types, TTL-expirable — the pinned memory is
// lost, contrary to "Pinned atoms are never selected" for eviction.
func TestRED_ConsolidationDropsUserPinOfLowerConfidenceMember(t *testing.T) {
	llm := newMockLLM("User prefers Go for backend services and tools")
	em := newConsolidationEM(t, llm)

	pinned := MemoryAtom{Text: "User prefers Go for backend services", SourceClass: SourceUserSaid, Confidence: 0.5}
	newer := MemoryAtom{Text: "User prefers Go for backend services and tools", SourceClass: SourceUserSaid, Confidence: 0.9}
	for _, a := range []MemoryAtom{pinned, newer} {
		if err := em.AddAtom(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	atoms, _ := em.List()
	for _, a := range atoms {
		if a.Text == pinned.Text {
			if err := em.PinAtom(a.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	merged, err := em.ConsolidateAtoms(context.Background())
	if err != nil || merged != 1 {
		t.Fatalf("precondition: expected one merged group, got %d (err=%v)", merged, err)
	}
	atoms, _ = em.List()
	if len(atoms) != 1 {
		t.Fatalf("precondition: expected one merged atom, got %+v", atoms)
	}
	if !atoms[0].Pin {
		t.Fatalf("consolidation dropped the user's pin: merged atom %+v is unpinned (evictable) although a group member was pinned", atoms[0])
	}
}
