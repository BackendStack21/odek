package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveMediaPath validates and resolves an agent-supplied media path before
// it is uploaded to Telegram.
//
// Allowed base directories are:
//   - the current working directory,
//   - the odek media directory (~/.odek/media), and
//   - the system temporary directory.
//
// The input path is expanded to an absolute, cleaned path, any symlinks are
// resolved, and the final resolved path must be a regular file inside one of
// the allowed base directories. The final path component itself must not be a
// symlink. This prevents a prompt-injected agent from exfiltrating arbitrary
// files such as /home/user/.ssh/id_rsa via MEDIA:... or send_message(file=...).
func ResolveMediaPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("media path is empty")
	}

	// Expand a leading ~ to the user's home directory.
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("media path: resolve home: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}

	// Resolve to an absolute, cleaned path.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("media path: resolve absolute: %w", err)
	}
	abs = filepath.Clean(abs)

	// The final component must not be a symlink and must be a regular file.
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("media path: lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("media path: symlinks are not allowed: %s", abs)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("media path: not a regular file: %s", abs)
	}

	// Resolve all symlinks in the path. Any symlink that escapes the allowlist
	// is caught by the containment check below.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("media path: resolve symlinks: %w", err)
	}
	resolved = filepath.Clean(resolved)

	allowed, err := mediaBaseDirs()
	if err != nil {
		return "", fmt.Errorf("media path: allowed dirs: %w", err)
	}

	for _, base := range allowed {
		if isPathInside(resolved, base) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("media path outside allowed directories: %s", resolved)
}

// mediaBaseDirs returns the resolved, cleaned allowed base directories for
// outbound media paths. Errors retrieving individual directories are ignored
// where safe to do so (a directory that cannot be located cannot contain a
// valid media file), but the current working directory and temp directory are
// always included.
func mediaBaseDirs() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}

	dirs := []string{cwd}

	if mediaDir, err := MediaDir(); err == nil {
		dirs = append(dirs, mediaDir)
	}

	dirs = append(dirs, os.TempDir())

	resolved := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = filepath.Clean(d)
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = filepath.Clean(real)
		}
		resolved = append(resolved, d)
	}
	return resolved, nil
}

// isPathInside reports whether child is equal to or inside parent, using
// filepath-aware separator matching to avoid false positives from path
// prefixes.
func isPathInside(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(parent, sep) {
		parent += sep
	}
	return strings.HasPrefix(child+sep, parent)
}
