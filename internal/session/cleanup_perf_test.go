package session

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRED_Session_CleanupBatchesVectorRemovalOutsideLocks(t *testing.T) {
	store := newTestStore(t)
	if err := store.InitVectorIndex(nil); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 8; i++ {
		sess, err := store.Create([]Message{{Role: "user", Content: "searchable conversation text"}}, "m", "t")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
	}
	var fired atomic.Int32
	store.OnDelete = func(string) { fired.Add(1) }

	before := store.Vec.saves
	n, err := store.Cleanup(time.Now().Add(time.Hour))
	if err != nil || n != len(ids) {
		t.Fatalf("Cleanup = %d, %v; want %d", n, err, len(ids))
	}
	if got := store.Vec.saves - before; got != 1 {
		t.Fatalf("Cleanup rewrote the vector store %d times, want 1", got)
	}
	if int(fired.Load()) != len(ids) {
		t.Fatalf("OnDelete fired %d times, want %d", fired.Load(), len(ids))
	}
	for _, id := range ids {
		if _, err := os.Stat(store.path(id)); !os.IsNotExist(err) {
			t.Fatalf("session %s file survived cleanup", id)
		}
	}
	res, _ := store.Vec.Search("searchable conversation text", 10)
	for _, r := range res {
		for _, id := range ids {
			if r.SessionID == id {
				t.Fatalf("deleted session %s still in vector index", id)
			}
		}
	}
	// Locks are free after the sweep.
	if _, err := store.Create([]Message{{Role: "user", Content: "after"}}, "m", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestVectorIndex_RemoveManyEmptyAndNotReady(t *testing.T) {
	vi := &VectorIndex{}
	if err := vi.RemoveMany(nil); err != nil {
		t.Fatal(err)
	}
	if err := vi.RemoveMany([]string{"x"}); err != nil {
		t.Fatal(err)
	}
}
