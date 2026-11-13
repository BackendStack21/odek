package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rh1112cWriteDense writes a legacy complete association graph over ids (the
// layout the unbounded task linker produced), in Persist's format, and
// returns the file size.
func rh1112cWriteDense(t *testing.T, dir string, ids []string) int64 {
	t.Helper()
	dense := map[string][]string{}
	for i, a := range ids {
		for j, b := range ids {
			if i != j {
				dense[a] = append(dense[a], b)
			}
		}
	}
	raw, err := json.MarshalIndent(dense, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "associations.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return int64(len(raw))
}

// rh1112cAtomFootprint is the atom footprint enforceCap compares with the cap.
func rh1112cAtomFootprint(em *ExtendedMemory) int64 {
	atoms, _ := em.store.List()
	var total int64
	for _, s := range buildSizedAtoms(em.store, atoms) {
		total += s.size + vectorCost(len(atoms))
	}
	return total
}

func rh1112cIDs(atoms []MemoryAtom, n int) []string {
	ids := make([]string, 0, n)
	for _, a := range atoms[:n] {
		ids = append(ids, a.ID)
	}
	return ids
}

// A store written before the association bound holds a dense legacy
// associations.json. The cap counts that file, so it must be pruned before
// the cap is judged: otherwise the add fails ("cannot free …") although the
// atoms alone fit with room to spare, and the file is never pruned because
// pruning happens only in the Persist the failed add never reaches.
func TestRED_LegacyDenseAssociationsDoNotWedgeTheStore(t *testing.T) {
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, 200)
	atoms, _ := rh1112MemOpen(t, dir, nil).store.List()
	assocBytes := rh1112cWriteDense(t, dir, rh1112cIDs(atoms, len(atoms)))

	em := rh1112MemOpen(t, dir, func(c *Config) { c.SemanticDedupThreshold = floatPtr(0) })
	em.testCapBytes = rh1112cAtomFootprint(em) + 256<<10 // atoms fit with 256 KiB to spare

	const text = "the user prefers short commit messages"
	err := em.AddAtom(context.Background(), MemoryAtom{Text: text, SourceClass: SourceUserSaid, Type: TypePreference})
	left, _ := em.store.List()
	stored := false
	for _, a := range left {
		stored = stored || a.Text == text
	}
	if err != nil || !stored {
		t.Fatalf("new atom not stored (err=%v, %d atoms left); associations.json %d B -> %d B",
			err, len(left), assocBytes, em.assoc.FileSize())
	}
	if em.assoc.FileSize() >= assocBytes {
		t.Fatalf("legacy associations.json was not pruned: %d B", em.assoc.FileSize())
	}
}

// When the evictable atoms do cover the legacy file, no live atom may be
// evicted to pay for association bytes a prune frees.
func TestRED_LegacyDenseAssociationsEvictNoLiveAtom(t *testing.T) {
	dir := t.TempDir()
	rh1112MemSeedStore(t, dir, 600)
	atoms, _ := rh1112MemOpen(t, dir, nil).store.List()
	assocBytes := rh1112cWriteDense(t, dir, rh1112cIDs(atoms, 100))

	em := rh1112MemOpen(t, dir, func(c *Config) { c.SemanticDedupThreshold = floatPtr(0) })
	const headroom = 256 << 10
	em.testCapBytes = rh1112cAtomFootprint(em) + headroom
	reads := 0
	recency := em.assoc.recency
	em.assoc.SetRecency(func() map[string]time.Time { reads++; return recency() })
	if err := em.AddAtom(context.Background(), MemoryAtom{
		Text: "the user prefers short commit messages", SourceClass: SourceUserSaid, Type: TypePreference,
	}); err != nil {
		t.Fatal(err)
	}
	left, _ := em.store.List()
	after := em.assoc.FileSize()
	if after >= headroom {
		t.Fatalf("fixture: the pruned associations.json (%d B) does not fit the %d B headroom", after, headroom)
	}
	if len(left) != 601 {
		t.Fatalf("%d live atoms evicted to pay for association bytes a prune frees (%d -> %d B)",
			601-len(left), assocBytes, after)
	}
	if reads != 1 {
		t.Fatalf("one add read the recency table %d times, want the single prune", reads)
	}
}

// Every agent builds its own MemoryManager, so a Telegram chat (one agent per
// message) or an `odek continue` run stamps all its atoms with the same turn.
// The temporal rule (|Δturn| <= 2, same session) then matches every earlier
// atom of the session. Links created per add and the related list written
// into atoms.json must stay bounded, so atoms.json grows linearly.
func TestRED_TemporalLinksBoundedPerSession(t *testing.T) {
	grow := func(n int) (related int, atomsJSON int64) {
		dir := t.TempDir()
		em := rh1112MemOpen(t, dir, func(c *Config) { c.SemanticDedupThreshold = floatPtr(0) })
		for i := 0; i < n; i++ {
			if err := em.AddAtom(context.Background(), MemoryAtom{
				Text:        fmt.Sprintf("chat fact %d: the user mentioned item-%d-%x", i, i, i*7919),
				SourceClass: SourceUserSaid, Type: TypeFact,
				Context: AtomContext{SessionID: "tg-1001", Turn: 1},
			}); err != nil {
				t.Fatal(err)
			}
		}
		atoms, _ := em.store.List()
		for _, a := range atoms {
			related = max(related, len(a.Context.RelatedAtomIDs))
		}
		info, err := os.Stat(filepath.Join(dir, "atoms.json"))
		if err != nil {
			t.Fatal(err)
		}
		return related, info.Size()
	}
	r1, s1 := grow(30)
	r2, s2 := grow(60)
	if r2 > maxLinksPerAtom || float64(s2) > 2.5*float64(s1) {
		t.Fatalf("one session's temporal links form a complete graph: related list %d -> %d, atoms.json %d -> %d B (%.1fx for 2x atoms)",
			r1, r2, s1, s2, float64(s2)/float64(s1))
	}
}

// When creation times cannot rank a dense cluster's links (unknown, or all
// equal), the tie-break must not send every atom's selection to the lowest
// ids: that turns them into hubs that keep the whole cluster linked.
func TestRED_PruneTiesDoNotBuildHubs(t *testing.T) {
	id := func(i int) string { return fmt.Sprintf("%032x", i+1) }
	same := time.Now().Add(-time.Hour)
	for _, recency := range []func() map[string]time.Time{
		nil,
		func() map[string]time.Time {
			m := map[string]time.Time{}
			for i := 0; i < 100; i++ {
				m[id(i)] = same
			}
			return m
		},
	} {
		a := NewAssociationsWithDir(t.TempDir())
		if recency != nil {
			a.SetRecency(recency)
		}
		for i := 0; i < 100; i++ {
			for j := i + 1; j < 100; j++ {
				a.Link(id(i), id(j))
			}
		}
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
		// Selections near the ends of the order are one-sided, so an atom
		// may keep a few links beyond its own selection, never a hub's worth.
		for i := 0; i < 100; i++ {
			got := len(a.Related(id(i)))
			if got == 0 || got > maxLinkFactor*maxLinksPerAtom {
				t.Fatalf("recency=%v: cluster atom %d keeps %d links after the prune", recency != nil, i, got)
			}
		}
		if got := len(a.Related(id(0))); got > maxLinksPerAtom {
			t.Fatalf("recency=%v: the lowest id keeps %d links", recency != nil, got)
		}
	}
}

// The prune trigger must see a dense cluster even when a large sparse part
// of the store dilutes the store-wide link average: legacy stores are pruned
// the next time associations are persisted.
func TestRED_DenseClusterInSparseStorePruned(t *testing.T) {
	dir := t.TempDir()
	a := NewAssociationsWithDir(dir)
	id := func(i int) string { return fmt.Sprintf("%032x", i+1) }
	const cluster, chain = 100, 3000
	created := map[string]time.Time{}
	base := time.Now().Add(-72 * time.Hour)
	for i := 0; i < cluster+chain+1; i++ {
		created[id(i)] = base.Add(time.Duration(i) * time.Minute)
	}
	a.SetRecency(func() map[string]time.Time { return created })
	for i := 0; i < cluster; i++ {
		for j := i + 1; j < cluster; j++ {
			a.Link(id(i), id(j))
		}
	}
	for i := cluster; i < cluster+chain; i++ {
		a.Link(id(i), id(i+1))
	}
	if err := a.Persist(); err != nil {
		t.Fatal(err)
	}
	links := 0
	for i := 0; i < cluster; i++ {
		got := len(a.Related(id(i)))
		if got == 0 {
			t.Fatalf("cluster atom %d lost every link", i)
		}
		links += got
	}
	if links/2 > maxLinksPerAtom*cluster {
		t.Fatalf("dense cluster keeps %d links after Persist, bound is %d", links/2, maxLinksPerAtom*cluster)
	}
	if got := len(a.Related(id(cluster - 1))); got > maxLinksPerAtom {
		t.Fatalf("newest cluster atom keeps %d links", got)
	}
	for i := cluster + 1; i < cluster+chain; i++ {
		if got := len(a.Related(id(i))); got != 2 {
			t.Fatalf("chain atom %d holds %d links, want its 2 untouched", i, got)
		}
	}
}
