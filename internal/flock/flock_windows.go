//go:build windows

package flock

import (
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

func unlockFile(fd int) error {
	h := windows.Handle(fd)
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(h, 0, 1, 0, &overlapped)
}
