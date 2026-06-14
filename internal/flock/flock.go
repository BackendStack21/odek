// Package flock provides a portable advisory file lock.
//
// Lock opens or creates a lock file and acquires an exclusive lock on it.
// The returned release function must be called to unlock and close the file.
// The lock is advisory: it only serializes callers that also use this package
// (or otherwise cooperate on the same lock file).
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
