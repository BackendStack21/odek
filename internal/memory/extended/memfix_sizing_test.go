package extended

import (
	"context"
	"fmt"
	"testing"
)

// Sizing the store from one metadata read yields exactly the sizes the
// per-atom AtomSize path reported, including the text-length fallback for an
// atom that is not stored, so eviction order and survivors are unchanged.
func TestBuildSizedAtomsMatchesPerAtomSizing(t *testing.T) {
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, 12)
	em := rh1112MemOpen(t, dir, nil)
	atoms, err := em.store.List()
	if err != nil {
		t.Fatal(err)
	}
	atoms = append(atoms, MemoryAtom{ID: fmt.Sprintf("%032x", 999), Text: "ghost atom"})
	got := buildSizedAtoms(em.store, atoms)
	if len(got) != len(atoms) {
		t.Fatalf("got %d sized atoms, want %d", len(got), len(atoms))
	}
	for i, a := range atoms {
		want, err := em.store.AtomSize(a.ID)
		if err != nil {
			want = int64(len(a.Text)) + 256
		}
		if got[i].atom.ID != a.ID || got[i].size != want {
			t.Fatalf("atom %s: size %d, want %d", a.ID, got[i].size, want)
		}
	}
}

// Under a tight cap the lowest-retention unpinned atoms go first and pinned
// atoms always survive.
func TestCapEvictionKeepsPinnedAndNewAtom(t *testing.T) {
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, 30)
	em := rh1112MemOpen(t, dir, func(c *Config) { c.SemanticDedupThreshold = floatPtr(0) })
	pinned := fmt.Sprintf("%032x", 1)
	if err := em.store.Pin(pinned, true); err != nil {
		t.Fatal(err)
	}
	size, _ := em.store.Size()
	em.testCapBytes = size
	const text = "a brand new preference worth keeping"
	if err := em.AddAtom(context.Background(), MemoryAtom{
		Text: text, SourceClass: SourceUserSaid, Type: TypePreference,
	}); err != nil {
		t.Fatal(err)
	}
	left, _ := em.store.List()
	if len(left) >= 31 {
		t.Fatalf("nothing evicted under a tight cap: %d atoms", len(left))
	}
	hasPinned, hasNew := false, false
	for _, a := range left {
		hasPinned = hasPinned || a.ID == pinned
		hasNew = hasNew || a.Text == text
	}
	if !hasPinned || !hasNew {
		t.Fatalf("pinned survived=%v new stored=%v", hasPinned, hasNew)
	}
}
