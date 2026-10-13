package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRED_DeleteRemovesDanglingSymlinkSession: a session path that is a
// symlink (including a dangling one) is listed by the store but Exists uses
// os.Stat, which follows the link and reports a dangling one as absent — so
// `odek session delete` refused and left the link on disk with no CLI way to
// remove it. A symlink at the session path is a session artifact and must be
// deletable.
func TestRED_DeleteRemovesDanglingSymlinkSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := "20260101-danglingxx"
	link := filepath.Join(dir, id+".json")
	if err := os.Symlink(filepath.Join(dir, "no-such-target.json"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The artifact exists on disk even though it does not resolve.
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("fixture symlink missing: %v", err)
	}
	if !store.HasRemnants(id) {
		t.Fatal("a symlink at the session path is a leftover the operator must be able to purge")
	}
	if err := store.Delete(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink survived delete: %v", err)
	}
}
