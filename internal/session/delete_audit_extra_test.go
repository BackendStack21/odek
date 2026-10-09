package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRemovesAuditLogAndCorruptCopies(t *testing.T) {
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
	log := filepath.Join(store.Dir(), "audit", sess.ID+".json")
	corrupt := log + ".corrupt-20260101T000000.000000000"
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := store.Cleanup(time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	for _, p := range []string{log, corrupt} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s survived cleanup", p)
		}
	}
}

func TestAuditStoreRemoveMissingAndInvalid(t *testing.T) {
	a := NewAuditStore(t.TempDir())
	if err := a.Remove("20260101-aaaaaa"); err != nil {
		t.Fatalf("missing log should be nil: %v", err)
	}
	if err := a.Remove("../evil"); err == nil {
		t.Fatal("invalid id accepted")
	}
}
