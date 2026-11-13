package extended

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func pruneTriggerClique(n int) map[string]map[string]struct{} {
	a := NewAssociations()
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			a.Link(fmt.Sprintf("%032x", i+1), fmt.Sprintf("%032x", j+1))
		}
	}
	return a.links
}

// The trigger fires once the links between crowded atoms exceed
// maxLinkFactor*maxLinksPerAtom per crowded atom: a complete cluster of k
// atoms holds k(k-1)/2 such links against a line of 48k.
func TestNeedsPruneCliqueBoundary(t *testing.T) {
	if needsPrune(pruneTriggerClique(97)) {
		t.Fatal("a 97-atom clique (96 links per atom) is within the bound")
	}
	if !needsPrune(pruneTriggerClique(98)) {
		t.Fatal("a 98-atom clique must trigger a prune")
	}
	hub := NewAssociations()
	for i := 2; i < 5000; i++ {
		hub.Link(fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", i))
	}
	if needsPrune(hub.links) {
		t.Fatal("a hub of sparse atoms has nothing to prune")
	}
	if needsPrune(nil) {
		t.Fatal("an empty map has nothing to prune")
	}
}

// Whatever the graph, a freshly pruned map is below the trigger (the next
// persist reads no recency), keeps every linked atom linked, and stays within
// maxLinksPerAtom links per linked atom.
func TestPrunedMapIsBelowTheTrigger(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 12; round++ {
		n := 200 + rng.Intn(250)
		ids := make([]string, n)
		created := map[string]time.Time{}
		base := time.Now().Add(-time.Hour)
		for i := range ids {
			ids[i] = fmt.Sprintf("%032x", i+1)
			// Some atoms share a timestamp so the id tie-break is exercised.
			created[ids[i]] = base.Add(time.Duration(rng.Intn(n)) * time.Second)
		}
		reads := 0
		a := NewAssociationsWithDir(t.TempDir())
		a.SetRecency(func() map[string]time.Time { reads++; return created })
		// A dense cluster, a few hubs and random sparse links.
		cluster := 120 + rng.Intn(50)
		for i := 0; i < cluster; i++ {
			for j := i + 1; j < cluster; j++ {
				if rng.Intn(10) > 0 {
					a.Link(ids[i], ids[j])
				}
			}
		}
		for h := 0; h < 3; h++ {
			for k := 0; k < n/2; k++ {
				a.Link(ids[rng.Intn(n)], ids[rng.Intn(n)])
			}
		}
		linked := map[string]bool{}
		for id, rel := range a.links {
			if len(rel) > 0 {
				linked[id] = true
			}
		}
		if !needsPrune(a.links) {
			t.Fatalf("round %d: fixture is below the trigger", round)
		}
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
		if needsPrune(a.links) {
			t.Fatalf("round %d: a freshly pruned map is still past the trigger", round)
		}
		total := 0
		for id := range linked {
			got := len(a.Related(id))
			if got == 0 {
				t.Fatalf("round %d: atom %s lost every link", round, id)
			}
			total += got
		}
		if total/2 > maxLinksPerAtom*len(linked) {
			t.Fatalf("round %d: %d links over %d atoms", round, total/2, len(linked))
		}
		before := reads
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
		if reads != before {
			t.Fatalf("round %d: re-persisting a pruned map read recency again", round)
		}
	}
}
