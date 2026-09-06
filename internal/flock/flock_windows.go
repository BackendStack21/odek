//go:build windows

package flock

import (
	"errors"

	"golang.org/x/sys/windows"
)

func lockFile(fd int) error {
	h := windows.Handle(fd)
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	)
}

// tryLockFile attempts a non-blocking exclusive lock, mapping the immediate
// lock-violation failure to ErrLocked.
func tryLockFile(fd int) error {
	h := windows.Handle(fd)
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrLocked
	}
	return err
}

func unlockFile(fd int) error {
	h := windows.Handle(fd)
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(h, 0, 1, 0, &overlapped)
}
