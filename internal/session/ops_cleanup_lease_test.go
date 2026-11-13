package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func opsAged(t *testing.T, s *Store, task string) *Session {
	t.Helper()
	sess, err := s.Create([]Message{{Role: "user", Content: task}}, "m", task)
	if err != nil {
		t.Fatal(err)
	}
	sess.UpdatedAt = time.Now().Add(-48 * time.Hour).UTC()
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

// Cleanup skips a leased session and removes the rest; a stale lock file with
// no holder does not pin a session; ExecutionLeased reflects the lease.
func TestOpsCleanupSkipsLeasedSessions(t *testing.T) {
	for _, withIndex := range []bool{true, false} {
		s, err := NewStoreWithDir(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		leased, stale := opsAged(t, s, "leased"), opsAged(t, s, "stale")
		if err := os.WriteFile(filepath.Join(s.dir, "."+stale.ID+".run.lock"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if !withIndex {
			_ = os.Remove(filepath.Join(s.dir, "index.json"))
		}
		release, err := s.AcquireExecution(context.Background(), leased.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !s.ExecutionLeased(leased.ID) || s.ExecutionLeased(stale.ID) {
			t.Fatal("ExecutionLeased does not match the held leases")
		}
		n, err := s.Cleanup(time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index=%v: deleted %d, want 1", withIndex, n)
		}
		if _, err := os.Stat(s.Path(leased.ID)); err != nil {
			t.Fatalf("leased session removed: %v", err)
		}
		if _, err := os.Stat(s.Path(stale.ID)); !os.IsNotExist(err) {
			t.Fatalf("stale-lock session not removed: %v", err)
		}
		release()
		if n, _ := s.Cleanup(time.Now().Add(-time.Hour)); n != 1 {
			t.Fatalf("released session not removed on the next sweep: %d", n)
		}
	}
}
