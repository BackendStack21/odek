package extended

import (
	"testing"
	"time"
)

// NewForMaintenance must not evict expired quarantine entries on open, so a
// reporting command (stats, quarantine, prune --dry-run) changes nothing
// and a dry run counts what a real prune would evict.
func TestRED_NewForMaintenanceDoesNotEvictOnOpen(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.QuarantineTTLDays = 7
	q := NewQuarantine(dir)
	old := MemoryAtom{ID: "fedcba9876543210fedcba9876543210", Text: "stale", Type: TypeFact, SourceClass: SourceWeb, CreatedAt: time.Now().UTC()}
	if err := q.StoreWithReason(old, "tainted"); err != nil {
		t.Fatal(err)
	}
	entries, _ := q.loadAtomsLocked()
	for i := range entries {
		entries[i].QuarantinedAt = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	if err := q.saveLocked(entries); err != nil {
		t.Fatal(err)
	}

	em := NewForMaintenance(dir, nil, cfg)
	defer em.Close()
	if got, _ := em.ListQuarantineEntries(); len(got) != 1 {
		t.Fatalf("maintenance open evicted the entry: %d left", len(got))
	}
	rep, err := em.Prune(time.Now().UTC(), true)
	if err != nil || rep.ExpiredQuarantine != 1 {
		t.Fatalf("dry run: %+v %v; want 1 expired quarantine entry counted", rep, err)
	}
	if got, _ := em.ListQuarantineEntries(); len(got) != 1 {
		t.Fatal("dry run evicted the entry")
	}
	rep, _ = em.Prune(time.Now().UTC(), false)
	if rep.ExpiredQuarantine != 1 {
		t.Fatalf("prune evicted %d, want 1", rep.ExpiredQuarantine)
	}

	// New (the agent path) still evicts on open.
	q2 := NewQuarantine(t.TempDir())
	_ = q2
	em2 := New(dir, nil, cfg)
	defer em2.Close()
	if got, _ := em2.ListQuarantineEntries(); len(got) != 0 {
		t.Fatalf("agent open left %d expired entries", len(got))
	}
}
