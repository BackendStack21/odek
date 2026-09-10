package session

import (
	"errors"
	"os"
	"testing"
)

func TestDeletedSnapshotCannotRecreateSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "task")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A different store models retention or deletion by another process.
	other, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []*Session{sess, loaded} {
		if err = store.SaveNoIndex(snapshot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale incremental save: %v", err)
		}
		if err = store.Save(snapshot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale final save: %v", err)
		}
	}
	if _, err = store.Load(sess.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted session reappeared: %v", err)
	}
}
