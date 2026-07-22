package extended

import (
	"sync"
	"testing"
	"time"
)

func TestQuarantineEvictExpiredByAge(t *testing.T) {
	q := NewQuarantine(t.TempDir())
	old := quarantineEntry{
		MemoryAtom:    MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "old", SourceClass: SourceWeb},
		QuarantinedAt: time.Now().UTC().AddDate(0, 0, -2),
	}
	newID, _ := generateAtomID()
	recent := quarantineEntry{
		MemoryAtom:    MemoryAtom{ID: newID, Text: "new", SourceClass: SourceWeb},
		QuarantinedAt: time.Now().UTC(),
	}
	q.mu.Lock()
	entries := []quarantineEntry{old, recent}
	if err := q.saveLocked(entries); err != nil {
		q.mu.Unlock()
		t.Fatal(err)
	}
	q.mu.Unlock()

	removed, err := q.EvictExpired(1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("expected 1 evicted, got %d", removed)
	}
	atoms, _ := q.List()
	if len(atoms) != 1 {
		t.Errorf("expected 1 atom remaining, got %d", len(atoms))
	}
}

func TestQuarantinePromoteAndForget(t *testing.T) {
	q := NewQuarantine(t.TempDir())
	id, _ := generateAtomID()
	atom := MemoryAtom{ID: id, Text: "promote me", SourceClass: SourceWeb}
	if err := q.Store(atom); err != nil {
		t.Fatal(err)
	}
	got, err := q.Promote(id)
	if err != nil {
		t.Fatalf("Promote failed: %v", err)
	}
	if got.Text != atom.Text {
		t.Errorf("Promote returned %q, want %q", got.Text, atom.Text)
	}
	if err := q.Forget(id); err != nil {
		t.Fatalf("Forget failed: %v", err)
	}
	atoms, _ := q.List()
	if len(atoms) != 0 {
		t.Errorf("expected 0 atoms after forget, got %d", len(atoms))
	}
	if _, err := q.Promote(id); err == nil {
		t.Error("expected Promote to fail after forget")
	}
}

func TestQuarantineInvalidID(t *testing.T) {
	q := NewQuarantine(t.TempDir())
	if err := q.Store(MemoryAtom{ID: "../bad", Text: "x"}); err == nil {
		t.Error("expected Store to reject invalid ID")
	}
	if _, err := q.Promote("../bad"); err == nil {
		t.Error("expected Promote to reject invalid ID")
	}
	if err := q.Forget("../bad"); err == nil {
		t.Error("expected Forget to reject invalid ID")
	}
}

func TestQuarantineListSortsNewestFirst(t *testing.T) {
	q := NewQuarantine(t.TempDir())
	id1, _ := generateAtomID()
	id2, _ := generateAtomID()
	q.Store(MemoryAtom{ID: id1, Text: "first", SourceClass: SourceWeb})
	q.Store(MemoryAtom{ID: id2, Text: "second", SourceClass: SourceWeb})
	atoms, _ := q.List()
	if len(atoms) != 2 {
		t.Fatal("expected 2 atoms")
	}
	if atoms[0].ID != id2 {
		t.Error("expected newest atom first")
	}
}

func TestQuarantineTTLDisabled(t *testing.T) {
	q := NewQuarantine(t.TempDir())
	q.mu.Lock()
	entries := []quarantineEntry{{
		MemoryAtom:    MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "x", SourceClass: SourceWeb},
		QuarantinedAt: time.Now().UTC().AddDate(0, 0, -10),
	}}
	if err := q.saveLocked(entries); err != nil {
		q.mu.Unlock()
		t.Fatal(err)
	}
	q.mu.Unlock()

	removed, err := q.EvictExpired(0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("expected 0 evicted with TTL disabled, got %d", removed)
	}
}

// TestQuarantineConcurrentListWithTTL exercises concurrent List/ListEntries/
// EvictExpired calls with TTL eviction enabled. List paths may evict expired
// entries (a write), so they must hold the write lock — run with -race to
// catch regressions.
func TestQuarantineConcurrentListWithTTL(t *testing.T) {
	dir := t.TempDir()
	q := NewQuarantine(dir)
	q.SetTTLDays(1)
	q.mu.Lock()
	entries := []quarantineEntry{{
		MemoryAtom:    MemoryAtom{ID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", Text: "old", SourceClass: SourceWeb},
		QuarantinedAt: time.Now().UTC().AddDate(0, 0, -2),
	}}
	if err := q.saveLocked(entries); err != nil {
		q.mu.Unlock()
		t.Fatal(err)
	}
	q.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.List()
			_, _ = q.ListEntries()
			_, _ = q.EvictExpired(1)
		}()
	}
	wg.Wait()
	atoms, err := q.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(atoms) != 0 {
		t.Errorf("expected expired atom evicted, got %d", len(atoms))
	}
}
