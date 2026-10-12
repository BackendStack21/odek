package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/flock"
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

// A run that opened the lock path before a delete unlinked it must not end up
// sharing the execution lock with a run that locks the recreated file.
func TestMemoryFix_StaleRunLockInodeNeverSharesOwnership(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "20261012-stalelock"
	path := filepath.Join(store.dir, "."+id+".run.lock")
	stale, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	store.removeRunLock(id) // unlinks the file stale still has open

	release, err := store.AcquireExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// The stale holder's lock lives on the orphaned inode and must not
	// contend with the owner; a second acquirer must still be excluded.
	unlock, err := flock.TryLockFile(stale)
	if err != nil {
		t.Fatalf("orphan inode lock unexpectedly contended: %v", err)
	}
	unlock()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel2()
	if r2, err := store.AcquireExecution(ctx2, id); err == nil {
		r2()
		t.Fatal("two runs hold the same session's execution lock")
	}
}
