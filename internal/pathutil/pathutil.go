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
// still enforce O_NOFOLLOW on it. Missing directory components are appended
// to the resolved nearest existing ancestor.
func ResolveDirSymlinks(path string) string {
	abs, err := CleanAbs(path)
	if err != nil {
		return path
	}

	// Resolve the nearest existing ancestor, then append the missing suffix.
	// Resolving only filepath.Dir(abs) falls back lexically when any parent is
	// absent, allowing an existing symlink ancestor to be hidden behind a new
	// child path that Docker will create on the host.
	cur := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		parent := filepath.Dir(cur)
		name := filepath.Base(cur)
		if parent == cur {
			return abs
		}
		suffix = append(suffix, name)
		cur = parent
	}
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
// nearest existing ancestor with the missing suffix appended.
func WithinRoot(root, candidate string) bool {
	absRoot, err := CleanAbs(root)
	if err != nil {
		return false
	}
	var resolvedRoot string
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		resolvedRoot = r
	} else {
		resolvedRoot = ResolveDirSymlinks(absRoot)
	}

	resolved := ResolveDirSymlinks(candidate)
	if resolved == resolvedRoot {
		return true
	}
	return strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator))
}
