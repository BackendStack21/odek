// Package memory provides persistent, agent-managed memory across sessions.
//
// # Architecture
//
// Three tiers:
//
//  1. Facts — Two typed files (user.md, env.md) with character caps, injected
//     into the system prompt as a frozen snapshot. Managed by the agent via the
//     memory tool (add/replace/remove/consolidate/read).
//
//  2. Buffer — In-memory ring buffer on the Session struct. One-line summaries
//     appended after each turn. Injected only when non-empty.
//
//  3. Episodes — LLM-extracted durable facts written after sessions with ≥3
//     turns. Searchable via memory(search=...).
//
// # Merge-on-Write
//
// When adding a fact, go-vector's RandomProjections provides a fast similarity
// check vs existing entries. cos > 0.7 = auto-merge, cos < 0.3 = auto-add,
// 0.3-0.7 = SimpleCall judgment. This saves ~80% of LLM calls on writes.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// File names for fact targets.
const (
	factsFileUser = "user.md"
	factsFileEnv  = "env.md"
)

// Default character caps for fact files.
const (
	defaultFactsLimitUser = 4000
	defaultFactsLimitEnv  = 8000
)

// entrySep is the delimiter between entries in a fact file.
const entrySep = "\n§\n"

// FactStore manages typed fact files (user.md and env.md) with character caps,
// duplicate prevention, and entry-level CRUD via substring matching.
//
// Concurrency: the per-instance mutex serializes the read+parse+modify+write of
// a single FactStore. Because each odek session builds its own MemoryManager /
// FactStore over the *same* memory directory, cross-instance serialization on a
// shared directory is provided one level up by the MemoryManager per-directory
// lock (factsDirLock) wrapping the full read-modify-write — without it,
// concurrent session-end writes would lose each other's updates. Disk writes go
// to a unique temp file + atomic rename, so concurrent writers never clobber a
// shared temp file.
type FactStore struct {
	mu      sync.Mutex
	dir     string
	capUser int
	capEnv  int
}

// NewFactStore creates a FactStore rooted at dir. Fact files are stored as
// dir/user.md and dir/env.md. Caps limit total file size.
func NewFactStore(dir string, capUser, capEnv int) *FactStore {
	if capUser <= 0 {
		capUser = defaultFactsLimitUser
	}
	if capEnv <= 0 {
		capEnv = defaultFactsLimitEnv
	}
	return &FactStore{
		dir:     dir,
		capUser: capUser,
		capEnv:  capEnv,
	}
}

// validateTarget returns the filename for a target, or an error if invalid.
func (f *FactStore) validateTarget(target string) (string, error) {
	switch target {
	case "user":
		return factsFileUser, nil
	case "env":
		return factsFileEnv, nil
	default:
		return "", fmt.Errorf("memory: invalid target %q (must be 'user' or 'env')", target)
	}
}

// path returns the full path for a target file.
func (f *FactStore) path(target string) string {
	filename, _ := f.validateTarget(target)
	return filepath.Join(f.dir, filename)
}

// cap returns the character cap for a target.
func (f *FactStore) cap(target string) int {
	switch target {
	case "env":
		return f.capEnv
	default:
		return f.capUser
	}
}

// sizeOf returns the total character size of entries including separators.
func (f *FactStore) sizeOf(entries []string) int {
	size := 0
	for i, e := range entries {
		if i > 0 {
			size += len(entrySep)
		}
		size += len(e)
	}
	return size
}

// Read returns the full content of a fact file. Returns empty string if the
// file doesn't exist yet.
func (f *FactStore) Read(target string) (string, error) {
	if _, err := f.validateTarget(target); err != nil {
		return "", err
	}
	data, err := os.ReadFile(f.path(target))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("memory: read %s: %w", target, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// readModifyWrite is the core read→modify→write pattern for fact files.
// The mutex is held for the entire read+modify+write cycle so that
// concurrent sessions don't lose each other's writes to the TOCTOU race
// between reading the old data and writing the new data.
//
// The modify function receives parsed entries and returns modified entries.
// If it returns nil entries, the write is skipped (no-op).
func (f *FactStore) readModifyWrite(target string, modify func([]string) ([]string, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	existing, err := f.Read(target)
	if err != nil {
		return err
	}
	entries := parseEntries(existing)
	result, err := modify(entries)
	if err != nil {
		return err
	}
	if result == nil {
		return nil // no-op
	}
	return f.writeEntries(target, result)
}

// Add appends a new entry to a fact file. Returns error if:
//   - target is invalid
//   - content is empty
//   - content already exists (dedup)
//   - adding would exceed the character cap
func (f *FactStore) Add(target, content string) error {
	if _, err := f.validateTarget(target); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory: empty content")
	}

	content = strings.TrimSpace(content)

	return f.readModifyWrite(target, func(entries []string) ([]string, error) {
		// Dedup: check if content already exists
		for _, e := range entries {
			if e == content {
				return nil, nil // silent dedup, skip write
			}
		}

		// Calculate new size
		newSize := 0
		for i, e := range entries {
			if i > 0 {
				newSize += len(entrySep)
			}
			newSize += len(e)
		}
		if len(entries) > 0 {
			newSize += len(entrySep)
		}
		newSize += len(content)

		maxCap := f.cap(target)
		if newSize > maxCap {
			return nil, fmt.Errorf("memory: adding entry (%d chars) would exceed cap (%d chars); current: %d, max: %d",
				len(content), maxCap, f.sizeOf(entries), maxCap)
		}

		// Append
		return append(entries, content), nil
	})
}

// Replace finds an entry by substring match and replaces it with new content.
// Returns error if the substring doesn't match exactly one entry.
func (f *FactStore) Replace(target, oldText, content string) error {
	if _, err := f.validateTarget(target); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory: empty replacement content")
	}
	if strings.TrimSpace(oldText) == "" {
		return fmt.Errorf("memory: empty old_text")
	}

	content = strings.TrimSpace(content)
	oldText = strings.TrimSpace(oldText)

	return f.readModifyWrite(target, func(entries []string) ([]string, error) {
		// Find matching entries
		var matchIdx int
		matchCount := 0
		for i, e := range entries {
			if strings.Contains(e, oldText) {
				matchIdx = i
				matchCount++
			}
		}

		if matchCount == 0 {
			return nil, fmt.Errorf("memory: no entry contains %q", oldText)
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("memory: %d entries contain %q — use a more specific old_text", matchCount, oldText)
		}

		// Calculate new size
		newSize := f.sizeOf(entries) - len(entries[matchIdx]) + len(content)
		maxCap := f.cap(target)
		if newSize > maxCap {
			return nil, fmt.Errorf("memory: replacement (%d chars) would exceed cap (%d chars)", newSize, maxCap)
		}

		entries[matchIdx] = content
		return entries, nil
	})
}

// Remove finds an entry by substring match and removes it. Returns error if
// the substring doesn't match exactly one entry.
func (f *FactStore) Remove(target, oldText string) error {
	if _, err := f.validateTarget(target); err != nil {
		return err
	}
	if strings.TrimSpace(oldText) == "" {
		return fmt.Errorf("memory: empty old_text")
	}

	oldText = strings.TrimSpace(oldText)

	return f.readModifyWrite(target, func(entries []string) ([]string, error) {
		var matchIdx int
		matchCount := 0
		for i, e := range entries {
			if strings.Contains(e, oldText) {
				matchIdx = i
				matchCount++
			}
		}

		if matchCount == 0 {
			return nil, fmt.Errorf("memory: no entry contains %q", oldText)
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("memory: %d entries contain %q — use a more specific old_text", matchCount, oldText)
		}

		// Remove by swapping with last and slicing
		entries[matchIdx] = entries[len(entries)-1]
		entries = entries[:len(entries)-1]
		return entries, nil
	})
}

// Entries returns the individual entries as a string slice.
func (f *FactStore) Entries(target string) ([]string, error) {
	if _, err := f.validateTarget(target); err != nil {
		return nil, err
	}
	existing, err := f.Read(target)
	if err != nil {
		return nil, err
	}
	return parseEntries(existing), nil
}

// writeEntries joins entries and writes them to disk atomically. It writes to a
// UNIQUE temp file in the same directory and renames it into place, so two
// FactStore instances writing the same directory concurrently can never clobber
// a shared temp file.
//
// Locking contract: callers must hold EITHER f.mu (normal mutation paths via
// readModifyWrite) OR the process-wide factsDirLock for this directory
// (Consolidate path in memory.go). These two locks are never acquired together,
// so there is no deadlock risk. factsDirLock provides the same cross-instance
// mutual exclusion that f.mu provides per-instance.
func (f *FactStore) writeEntries(target string, entries []string) error {
	content := strings.Join(entries, entrySep)
	path := f.path(target)

	tmp, err := os.CreateTemp(f.dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write([]byte(content)); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// parseEntries splits file content into individual entries.
func parseEntries(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	return strings.Split(content, entrySep)
}
