package extended

import (
	"fmt"
	"testing"
	"time"
)

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
