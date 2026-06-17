//go:build !unix

package telegram

import (
	"fmt"
	"os"
)

// verifyRegularFile is the non-Unix fallback. It uses Lstat instead of an
// atomic O_NOFOLLOW open; the Unix implementation provides stronger TOCTOU
// protection.
func verifyRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("media path: lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("media path: symlinks are not allowed: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("media path: not a regular file: %s", path)
	}
	return nil
}
