//go:build unix

package telegram

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// verifyRegularFile verifies that path points to a regular file and is not a
// symlink. It opens the path with O_NOFOLLOW and fstat's the resulting fd so
// the check is atomic: an attacker cannot swap a regular file for a symlink
// between an lstat and a later open (TOCTOU).
func verifyRegularFile(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if err == unix.ELOOP {
			return fmt.Errorf("media path: symlinks are not allowed: %s", path)
		}
		return fmt.Errorf("media path: open: %w", err)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("media path: fstat: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("media path: not a regular file: %s", path)
	}
	return nil
}
