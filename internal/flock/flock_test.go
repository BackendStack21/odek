package flock

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestLock_AcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	release, err := Lock(lockPath)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Lock file should exist with restricted permissions.
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("lock file is world/group accessible: %o", info.Mode().Perm())
	}

	release()

	// After release, the lock file may be left behind; that's fine.
}

func TestLock_SerializesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	counterPath := filepath.Join(dir, "counter")
	lockPath := filepath.Join(dir, "counter.lock")

	if err := os.WriteFile(counterPath, []byte("0"), 0600); err != nil {
		t.Fatalf("write counter: %v", err)
	}

	var wg sync.WaitGroup
	workers := 20
	increments := 50
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < increments; j++ {
				release, err := Lock(lockPath)
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				data, err := os.ReadFile(counterPath)
				if err != nil {
					release()
					t.Errorf("read counter: %v", err)
					return
				}
				n, err := strconv.Atoi(string(data))
				if err != nil {
					release()
					t.Errorf("parse counter: %v", err)
					return
				}
				if err := os.WriteFile(counterPath, []byte(strconv.Itoa(n+1)), 0600); err != nil {
					release()
					t.Errorf("write counter: %v", err)
					return
				}
				release()
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read final counter: %v", err)
	}
	got, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("parse final counter: %v", err)
	}
	want := workers * increments
	if got != want {
		t.Errorf("counter = %d, want %d (race detected)", got, want)
	}
}
