package extended

import (
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
