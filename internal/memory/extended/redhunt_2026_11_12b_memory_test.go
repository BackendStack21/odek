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

// rh1112bDense writes a complete association graph over n atoms into dir and
// returns the ids with their creation times.
func rh1112bDense(t *testing.T, dir string, n int) ([]string, map[string]time.Time) {
	t.Helper()
	ids := make([]string, n)
	created := map[string]time.Time{}
	base := time.Now().Add(-48 * time.Hour)
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
	return ids, created
}

func rh1112bLinkCount(a *Associations, ids []string) int {
	total := 0
	for _, id := range ids {
		total += len(a.Related(id))
	}
	return total / 2
}

func TestRED_PruneNeverStrandsAnAtom(t *testing.T) {
	dir := t.TempDir()
	ids, created := rh1112bDense(t, dir, 200)
	a := NewAssociationsWithDir(dir)
	a.SetRecency(func() map[string]time.Time { return created })
	if err := a.Persist(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if len(a.Related(id)) == 0 {
			t.Fatalf("atom %s lost every association", id)
		}
	}
	for _, id := range ids {
		for _, to := range a.Related(id) {
			if !slices.Contains(a.Related(to), id) {
				t.Fatalf("link %s -> %s is not symmetric", id, to)
			}
		}
	}
}

func TestRED_PruneLinksStayLinearInAtoms(t *testing.T) {
	counts := map[int]int{}
	for _, n := range []int{200, 400} {
		dir := t.TempDir()
		ids, created := rh1112bDense(t, dir, n)
		a := NewAssociationsWithDir(dir)
		a.SetRecency(func() map[string]time.Time { return created })
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
		counts[n] = rh1112bLinkCount(a, ids)
		if counts[n] > maxLinksPerAtom*n {
			t.Fatalf("n=%d keeps %d links, bound is %d", n, counts[n], maxLinksPerAtom*n)
		}
	}
	if float64(counts[400]) > 2.2*float64(counts[200]) {
		t.Fatalf("links grew superlinearly: %d at 200 atoms, %d at 400", counts[200], counts[400])
	}
}

func TestRED_PruneIsDeterministicAndIdempotent(t *testing.T) {
	// Equal creation times force the id tie-break to decide.
	build := func(reverse bool) (*Associations, []string) {
		dir := t.TempDir()
		ids, _ := rh1112bDense(t, dir, 150)
		same := time.Now().Add(-time.Hour)
		a := NewAssociationsWithDir(dir)
		a.SetRecency(func() map[string]time.Time {
			m := map[string]time.Time{}
			for _, id := range ids {
				m[id] = same
			}
			return m
		})
		if reverse {
			// Re-link in reverse order: insertion order must not matter.
			for i := len(ids) - 1; i >= 0; i-- {
				a.Link(ids[i], ids[(i+1)%len(ids)])
			}
		}
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
		return a, ids
	}
	a1, ids := build(false)
	a2, _ := build(true)
	for _, id := range ids {
		if !slices.Equal(a1.Related(id), a2.Related(id)) {
			t.Fatalf("pruned neighbours of %s differ between runs", id)
		}
	}
	before := map[string][]string{}
	for _, id := range ids {
		before[id] = a1.Related(id)
	}
	if err := a1.Persist(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !slices.Equal(a1.Related(id), before[id]) {
			t.Fatalf("re-persisting changed %s", id)
		}
	}
}

func TestRED_PersistReadsRecencyOnlyWhenPruning(t *testing.T) {
	dir := t.TempDir()
	reads := 0
	a := NewAssociationsWithDir(dir)
	a.SetRecency(func() map[string]time.Time { reads++; return nil })
	// A hub linked to many atoms is above the per-atom selection size but far
	// below the total-link trigger.
	hub := fmt.Sprintf("%032x", 1)
	for i := 2; i < 120; i++ {
		a.Link(hub, fmt.Sprintf("%032x", i))
		if err := a.Persist(); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 0 {
		t.Fatalf("recency table read %d times on a healthy store", reads)
	}
	if len(a.Related(hub)) < 100 {
		t.Fatalf("hub lost links without need: %d", len(a.Related(hub)))
	}

	// After a prune of a dense legacy store, further adds do not re-read.
	dir2 := t.TempDir()
	ids, created := rh1112bDense(t, dir2, 200)
	reads = 0
	b := NewAssociationsWithDir(dir2)
	b.SetRecency(func() map[string]time.Time { reads++; return created })
	if err := b.Persist(); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("prune read recency %d times, want 1", reads)
	}
	for i := 0; i < 20; i++ {
		b.Link(ids[i], ids[len(ids)-1-i])
		if err := b.Persist(); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("adds after a prune re-read recency (%d reads)", reads)
	}
}
