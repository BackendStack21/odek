package danger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// resolvePathTarget follows existing symlinks, including a dangling link's
// target and symlinked parents of a new file. Resolve .. after links, as the
// kernel does, instead of cleaning away a component before resolving it.
// This is a classification snapshot, not a filesystem execution boundary.
func resolvePathTarget(path string) (string, error) {
	if resolved, ok := memoResolved(path); ok {
		return resolved, nil
	}
	resolved, err := resolvePathTargetUncached(path)
	if err == nil {
		memoRememberResolved(path, resolved)
	}
	return resolved, err
}

func resolvePathTargetUncached(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := memoGetwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	parts := strings.Split(path, string(filepath.Separator))
	resolved := string(filepath.Separator)
	links := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		candidate := filepath.Join(resolved, part)
		if memoKnownDir(candidate) {
			resolved = candidate
			continue
		}
		st, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			resolved = candidate
			continue
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink == 0 {
			if st.IsDir() {
				memoRememberDir(candidate)
			}
			resolved = candidate
			continue
		}
		links++
		if links > 255 {
			return "", fmt.Errorf("too many symlinks in path")
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			resolved = string(filepath.Separator)
		}
		parts = append(strings.Split(target, string(filepath.Separator)), parts...)
	}
	return resolved, nil
}

// pathMemo remembers, for the duration of running analyses, the working
// directory, resolved path targets and which path components are plain
// directories. A command with thousands of path operands resolves the same
// parent chain and the same operands repeatedly; without the memo that is
// thousands of identical filesystem lookups. Classification is a snapshot, so
// reusing a lookup within one analysis is sound, and nothing survives the
// last active analysis: later calls see a fresh filesystem.
var pathMemo struct {
	mu     sync.Mutex
	active int
	dirs   map[string]struct{}
	paths  map[string]string
	exists map[string]bool
	cwd    string
	cwdErr error
	hasCwd bool
}

// beginPathMemo enables the memo until the returned function runs.
func beginPathMemo() func() {
	pathMemo.mu.Lock()
	pathMemo.active++
	pathMemo.mu.Unlock()
	return func() {
		pathMemo.mu.Lock()
		pathMemo.active--
		if pathMemo.active == 0 {
			pathMemo.dirs = nil
			pathMemo.paths = nil
			pathMemo.exists = nil
			pathMemo.hasCwd = false
			pathMemo.cwd, pathMemo.cwdErr = "", nil
		}
		pathMemo.mu.Unlock()
	}
}

func memoGetwd() (string, error) {
	pathMemo.mu.Lock()
	if pathMemo.active > 0 && pathMemo.hasCwd {
		cwd, err := pathMemo.cwd, pathMemo.cwdErr
		pathMemo.mu.Unlock()
		return cwd, err
	}
	pathMemo.mu.Unlock()
	cwd, err := os.Getwd()
	pathMemo.mu.Lock()
	if pathMemo.active > 0 {
		pathMemo.cwd, pathMemo.cwdErr, pathMemo.hasCwd = cwd, err, true
	}
	pathMemo.mu.Unlock()
	return cwd, err
}

func memoResolved(path string) (string, bool) {
	pathMemo.mu.Lock()
	defer pathMemo.mu.Unlock()
	if pathMemo.active == 0 {
		return "", false
	}
	resolved, ok := pathMemo.paths[path]
	return resolved, ok
}

func memoRememberResolved(path, resolved string) {
	pathMemo.mu.Lock()
	defer pathMemo.mu.Unlock()
	if pathMemo.active == 0 {
		return
	}
	if pathMemo.paths == nil {
		pathMemo.paths = make(map[string]string)
	}
	if len(pathMemo.paths) < 1<<16 {
		pathMemo.paths[path] = resolved
	}
}

// statPath is os.Stat; a variable so tests can count lookups.
var statPath = os.Stat

// memoPathExists reports whether path can be statted (following symlinks),
// remembering the answer for the duration of running analyses.
func memoPathExists(path string) bool {
	pathMemo.mu.Lock()
	if pathMemo.active > 0 {
		if ok, seen := pathMemo.exists[path]; seen {
			pathMemo.mu.Unlock()
			return ok
		}
	}
	pathMemo.mu.Unlock()
	_, err := statPath(path)
	ok := err == nil
	pathMemo.mu.Lock()
	if pathMemo.active > 0 {
		if pathMemo.exists == nil {
			pathMemo.exists = make(map[string]bool)
		}
		if len(pathMemo.exists) < 1<<16 {
			pathMemo.exists[path] = ok
		}
	}
	pathMemo.mu.Unlock()
	return ok
}

// absPath is filepath.Abs using the memoized working directory.
func absPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	cwd, err := memoGetwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, path), nil
}

func memoKnownDir(path string) bool {
	pathMemo.mu.Lock()
	defer pathMemo.mu.Unlock()
	if pathMemo.active == 0 {
		return false
	}
	_, ok := pathMemo.dirs[path]
	return ok
}

func memoRememberDir(path string) {
	pathMemo.mu.Lock()
	defer pathMemo.mu.Unlock()
	if pathMemo.active == 0 {
		return
	}
	if pathMemo.dirs == nil {
		pathMemo.dirs = make(map[string]struct{})
	}
	if len(pathMemo.dirs) < 1<<16 {
		pathMemo.dirs[path] = struct{}{}
	}
}
