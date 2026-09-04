package session

import (
	"strings"
	"testing"
	"time"
)

// TestStore_Latest_SkipsUnreadableNewestCandidate pins the documented
// contract of Latest: "return the first one whose file still loads".
//
// trimToFileCapLocked deliberately writes a session as-is when a single
// oversized message exceeds MaxSessionFileBytes (failing the save would
// lose data). Such a session is in the index and its file exists, but
// Load rejects it (file too large). Latest must skip it and return the
// newest session that still loads — not fail the whole lookup.
func TestStore_Latest_SkipsUnreadableNewestCandidate(t *testing.T) {
	store := newTestStore(t)

	// Healthy session, pushed into the past so it sorts after the victim.
	healthy, err := store.Create(
		[]Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello"}},
		"test-model", "healthy",
	)
	if err != nil {
		t.Fatalf("Create healthy: %v", err)
	}
	loaded, err := store.Load(healthy.ID)
	if err != nil {
		t.Fatalf("Load healthy: %v", err)
	}
	loaded.UpdatedAt = time.Now().UTC().Add(-time.Hour)
	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save healthy backdated: %v", err)
	}

	// Oversized single-system-message session: trimToFileCapLocked has
	// nothing droppable (start=1 >= len=1) and writes it as-is.
	orig := MaxSessionFileBytes
	MaxSessionFileBytes = 4 * 1024
	t.Cleanup(func() { MaxSessionFileBytes = orig })
	huge := strings.Repeat("x", 8*1024)
	oversized, err := store.Create(
		[]Message{{Role: "system", Content: huge}},
		"test-model", "oversized",
	)
	if err != nil {
		t.Fatalf("Create oversized (write-as-is path): %v", err)
	}
	if _, err := store.Load(oversized.ID); err == nil {
		t.Fatal("precondition: oversized session should fail Load (over cap)")
	}

	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest() must skip the unreadable newest candidate, got error: %v", err)
	}
	if latest == nil || latest.ID != healthy.ID {
		t.Fatalf("Latest() = %+v, want the healthy session %q", latest, healthy.ID)
	}
}
