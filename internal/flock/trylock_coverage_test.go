package flock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLockHappyPathAndErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	release, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}

	// Second TryLock on the held lock must fail with ErrLocked (wrapped or bare).
	_, err = TryLock(path)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock: want ErrLocked, got %v", err)
	}

	release()

	// After release the lock can be re-acquired.
	release2, err := TryLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}

func TestLockOpenErrorPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "test.lock")
	if _, err := Lock(path); err == nil {
		t.Fatal("Lock under nonexistent dir: want error, got nil")
	}
}

func TestTryLockOpenErrorPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "test.lock")
	_, err := TryLock(path)
	if err == nil {
		t.Fatal("TryLock under nonexistent dir: want error, got nil")
	}
	if errors.Is(err, ErrLocked) {
		t.Fatal("open error must not surface as ErrLocked")
	}
}
