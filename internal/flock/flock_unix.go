//go:build !windows

package flock

import (
	"errors"
	"syscall"
)

func lockFile(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX)
}

// tryLockFile attempts a non-blocking exclusive lock, mapping EWOULDBLOCK
// (EAGAIN) to ErrLocked.
func tryLockFile(fd int) error {
	err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlockFile(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_UN)
}
