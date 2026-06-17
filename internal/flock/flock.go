// Package flock provides a portable advisory file lock.
//
// Lock opens or creates a lock file and acquires an exclusive lock on it.
// The returned release function must be called to unlock and close the file.
//
// Advisory semantics
//
// The lock is advisory: it only serializes callers that also use this package
// (or otherwise cooperate on the same lock file). A non-cooperating process
// that has write access to the protected file can ignore the lock and read or
// write freely. For files containing sensitive data, rely on filesystem
// permissions (0700 directories, 0600 files) as the primary access control;
// flock is a coordination primitive, not a mandatory-access gate. The lock
// file itself is left on disk after release so the next caller can re-acquire
// it; it is created with 0600 permissions.
package flock

import (
	"fmt"
	"os"
)

// Lock acquires an exclusive advisory lock on path. It creates the lock file
// with 0600 permissions if it does not exist. The returned release function
// unlocks and closes the lock file; callers should defer it.
func Lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("flock: open: %w", err)
	}
	if err := lockFile(int(f.Fd())); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock: lock: %w", err)
	}
	return func() {
		unlockFile(int(f.Fd()))
		f.Close()
	}, nil
}
