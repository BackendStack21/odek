package flock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockFileOwnershipAndContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	open := func() *os.File {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	first, second := open(), open()
	unlock, err := TryLockFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := TryLockFile(second); !errors.Is(err, ErrLocked) {
		if release != nil {
			release()
		}
		unlock()
		t.Fatalf("competing lock: %v", err)
	}
	unlock()
	if _, err := first.WriteString("still owned by caller"); err != nil {
		t.Fatalf("unlock closed caller's file: %v", err)
	}
	release, err := TryLockFile(second)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if release, err := TryLockFile(first); err == nil || errors.Is(err, ErrLocked) || release != nil {
		t.Fatalf("closed descriptor: release=%v err=%v", release != nil, err)
	}
}
