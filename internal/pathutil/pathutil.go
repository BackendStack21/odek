// Package pathutil provides small, security-critical helpers for path
// confinement and symlink-aware resolution. These primitives are used by
// multiple packages (resource resolver, sandbox volume validation, file tools,
// etc.) so they are promoted here to avoid drifting near-identical copies.
package pathutil

import (
	"os"
	"path/filepath"
	"strings"
)

// CleanAbs returns the absolute, cleaned form of path. If the absolute path
// cannot be determined, it returns the error.
func CleanAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// ResolveDirSymlinks returns the absolute, cleaned path with all directory
// symlinks resolved. The final path component is left untouched so callers can
// still enforce O_NOFOLLOW on it. If a directory component does not exist,
// the original absolute path is returned so the caller can produce a sensible
// "not found" error.
func ResolveDirSymlinks(path string) string {
	abs, err := CleanAbs(path)
	if err != nil {
		return path
	}

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return abs
	}
	return filepath.Join(resolvedDir, base)
}

// WithinRoot reports whether candidate resolves to a path inside root.
// Directory symlinks in candidate are resolved before comparison so a symlinked
// directory outside the workspace cannot bypass confinement; the final
// component is kept unresolved so symlinks to files inside the workspace are
// still visible to callers that reject symlink final components separately.
// The check is separator-aware so "/foo" does not match "/foobar".
//
// If root cannot be symlink-resolved (e.g. it does not exist yet in a test or
// for a not-yet-created working directory), the comparison falls back to the
// lexical absolute path, preserving the original sandbox semantics where the
// resolved re-check was optional.
func WithinRoot(root, candidate string) bool {
	absRoot, err := CleanAbs(root)
	if err != nil {
		return false
	}
	resolvedRoot := absRoot
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		resolvedRoot = r
	}

	resolved := ResolveDirSymlinks(candidate)
	if resolved == resolvedRoot {
		return true
	}
	return strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator))
}
