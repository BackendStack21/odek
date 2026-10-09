package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRED_DeleteRemovesAuditLog(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{{Role: "system", Content: "s"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuditStore(store.Dir())
	if err := a.RecordIngest(sess.ID, 1, "https://x", "body"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "audit", sess.ID+".json")); err == nil {
		t.Fatalf("audit log survived session delete")
	}
}
