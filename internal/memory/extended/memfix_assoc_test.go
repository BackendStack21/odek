package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A store written with the old complete task graph loads without error and is
// pruned to the per-atom bound, keeping the most recent neighbours, the next
// time the associations are persisted.
func TestAssociationsLegacyDenseGraphPrunedOnPersist(t *testing.T) {
	dir := t.TempDir()
	const n = 120
	ids := make([]string, n)
	created := map[string]time.Time{}
	base := time.Now().Add(-time.Hour)
	dense := map[string][]string{}
	for i := range ids {
		ids[i] = fmt.Sprintf("%032x", i+1)
		created[ids[i]] = base.Add(time.Duration(i) * time.Minute)
	}
	for i, a := range ids {
		for j, b := range ids {
			if i != j {
				dense[a] = append(dense[a], b)
			}
		}
	}
	raw, _ := json.Marshal(dense)
	if err := os.WriteFile(filepath.Join(dir, "associations.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewAssociationsWithDir(dir)
	if got := len(a.Related(ids[0])); got != n-1 {
		t.Fatalf("legacy graph should load whole, got %d links", got)
	}
	a.SetRecency(func() map[string]time.Time { return created })
	if err := a.Persist(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, id := range ids {
		got := len(a.Related(id))
		if got == 0 {
			t.Fatalf("atom %s lost every link", id)
		}
		total += got
	}
	if total/2 > maxLinksPerAtom*n {
		t.Fatalf("%d links remain, bound is %d", total/2, maxLinksPerAtom*n)
	}
	// The newest atom keeps its most recent neighbours.
	newest := a.Related(ids[n-1])
	if len(newest) == 0 || !slices.Contains(newest, ids[n-2]) {
		t.Fatalf("newest atom lost its most recent neighbour: %v", newest)
	}
	for _, id := range ids {
		for _, to := range a.Related(id) {
			if !slices.Contains(a.Related(to), id) {
				t.Fatalf("link %s -> %s is not symmetric", id, to)
			}
		}
	}
	if a.FileSize() == 0 {
		t.Fatal("FileSize reports an empty associations.json")
	}
}

// associations.json counts toward the store size the cap is enforced against.
func TestSizeCountsAssociationsFile(t *testing.T) {
	dir := t.TempDir()
	em := rh1112MemOpen(t, dir, nil)
	before := em.Size()
	em.assoc.Link(fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2))
	if err := em.assoc.Persist(); err != nil {
		t.Fatal(err)
	}
	if em.Size() <= before {
		t.Fatalf("Size %d did not grow after associations.json was written (was %d)", em.Size(), before)
	}
}
