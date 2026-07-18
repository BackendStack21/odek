package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BackendStack21/odek/internal/danger"
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
// symlink.
//
// Additionally, paths inside well-known secret subtrees (for example ~/.ssh,
// ~/.aws, ~/.gnupg, ~/.odek trust anchors) and any file whose basename starts
// with ".env" are rejected even when the containing base directory is
// otherwise allowed. This prevents a prompt-injected agent from exfiltrating
// arbitrary files such as /home/user/.ssh/id_rsa via MEDIA:... or
// send_message(file=...), especially when the bot was launched from $HOME or /.
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
	// On Unix this is done with an atomic O_NOFOLLOW open + fstat to prevent
	// a TOCTOU race where a directory is swapped for a symlink between lstat
	// and the subsequent read.
	if err := verifyRegularFile(abs); err != nil {
		return "", err
	}

	// Resolve any symlinks in the path (but not the final component, which we
	// already verified is a regular file). Any symlink that escapes the
	// allowlist is caught by the containment check below.
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
			if err := checkMediaPathSensitivity(resolved); err != nil {
				return "", err
			}
			return resolved, nil
		}
	}

	return "", fmt.Errorf("media path outside allowed directories: %s", resolved)
}

// checkMediaPathSensitivity rejects well-known secret subtrees and .env* files
// that must never be uploaded as Telegram media, even when they sit inside an
// otherwise allowed base directory (e.g. CWD == $HOME).
func checkMediaPathSensitivity(resolved string) error {
	// Re-use the same path sensitivity model as the danger classifier. Anything
	// ranked at system_write or above (~/.ssh, ~/.aws, ~/.gnupg, /etc, ~/.odek
	// trust anchors, etc.) is treated as too sensitive for outbound media.
	cls := danger.ClassifyPath(resolved)
	if danger.Rank(cls) >= danger.Rank(danger.SystemWrite) {
		return fmt.Errorf("media path: rejected sensitive path (%s): %s", cls, resolved)
	}

	// Also reject .env* files anywhere in the tree — they routinely contain API
	// keys and secrets and may live inside an allowed project directory.
	base := strings.ToLower(filepath.Base(resolved))
	if strings.HasPrefix(base, ".env") {
		return fmt.Errorf("media path: rejected .env file: %s", resolved)
	}

	return nil
}

// BroadBaseWarning returns a non-empty warning when the current working
// directory is an unusually broad base ($HOME or /). Launching the Telegram
// bot from these directories makes the CWD allowlist cover a large amount of
// sensitive filesystem territory, so callers should surface this in any
// approval prompt.
func BroadBaseWarning() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	cwd = filepath.Clean(cwd)

	if cwd == "/" {
		return "The bot's working directory is /, so the media allowlist covers the whole filesystem."
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	home = filepath.Clean(home)
	if cwd == home {
		return "The bot's working directory is $HOME, so the media allowlist covers your entire home directory."
	}

	return ""
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
