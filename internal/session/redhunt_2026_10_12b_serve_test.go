package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// An audit store bound to its sessions must not write for a session that was
// deleted, even through a different AuditStore instance than the one Delete
// uses to remove the log.
func TestRED_AuditWriteAfterDeleteIsDropped(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	audit := NewAuditStore(store.Dir()).RequireSessionFile()
	if err := audit.RecordIngest(sess.ID, 1, "tool:shell", "x"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	_ = audit.RecordIngest(sess.ID, 1, "tool:shell", "late")
	_ = audit.RecordTurn(sess.ID, AuditTurn{Turn: 1, UserMessage: "late"})
	if _, err := os.Stat(filepath.Join(store.Dir(), "audit", sess.ID+".json")); err == nil {
		t.Fatal("audit log re-created after the session was deleted")
	}
}

// Concurrent appends and Delete never leave a log behind.
func TestRED_AuditDeleteRaceLeavesNoLog(t *testing.T) {
	for i := 0; i < 30; i++ {
		store, err := NewStoreWithDir(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sess, err := store.Create(nil, "m", "t")
		if err != nil {
			t.Fatal(err)
		}
		audit := NewAuditStore(store.Dir()).RequireSessionFile()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = audit.RecordIngest(sess.ID, 1, "tool:x", "c")
			}
		}()
		go func() { defer wg.Done(); _ = store.Delete(sess.ID) }()
		wg.Wait()
		if _, err := os.Stat(filepath.Join(store.Dir(), "audit", sess.ID+".json")); err == nil {
			t.Fatal("audit log survived Delete")
		}
	}
}
