package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A run lock held by a live run is kept when its session is deleted, and
// removed once nobody holds it.
func TestMemoryFix_DeleteKeepsHeldRunLock(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "20261012-heldlock"
	if err := store.Save(&Session{ID: id, Task: "x", Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	release, err := store.AcquireExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(store.dir, "."+id+".run.lock")
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("held lock file must survive Delete: %v", err)
	}
	release()
	// A later delete of the same id (idempotent) now sweeps the lock.
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("released lock file should be removed")
	}
}
