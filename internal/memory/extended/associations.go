package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/session"
)

// Associations stores bidirectional links between related atoms.
type Associations struct {
	mu      sync.RWMutex
	dir     string
	links   map[string]map[string]struct{}
	exists  func(id string) bool
	removed map[string]struct{}
}

// NewAssociations returns an in-memory association map.
func NewAssociations() *Associations {
	return &Associations{
		links: make(map[string]map[string]struct{}),
	}
}

// NewAssociationsWithDir returns an Associations that persists to dir.
func NewAssociationsWithDir(dir string) *Associations {
	a := NewAssociations()
	a.dir = dir
	_ = a.Load()
	return a
}

// Link creates an undirected link between two atoms.
func (a *Associations) Link(fromID, toID string) {
	if a == nil || fromID == "" || toID == "" || fromID == toID {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.removed, fromID)
	delete(a.removed, toID)
	a.linkOne(fromID, toID)
	a.linkOne(toID, fromID)
}

func (a *Associations) linkOne(fromID, toID string) {
	if a.links[fromID] == nil {
		a.links[fromID] = make(map[string]struct{})
	}
	a.links[fromID][toID] = struct{}{}
}

// Related returns the atom IDs linked to id, sorted.
func (a *Associations) Related(id string) []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	related, ok := a.links[id]
	if !ok || len(related) == 0 {
		return nil
	}
	out := make([]string, 0, len(related))
	for toID := range related {
		out = append(out, toID)
	}
	sort.Strings(out)
	return out
}

// RemoveAtom removes all links to and from an atom.
func (a *Associations) RemoveAtom(id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.removed == nil {
		a.removed = make(map[string]struct{})
	}
	a.removed[id] = struct{}{}
	delete(a.links, id)
	for fromID, related := range a.links {
		delete(related, id)
		if len(related) == 0 {
			delete(a.links, fromID)
		}
	}
}

// Persist saves the association map to disk. The file is shared with other
// processes (a CLI forget/prune beside a running daemon), so the write runs
// under a cross-process lock and merges with what is on disk: links another
// process added are kept, links to atoms this process removed are dropped,
// and links this process holds for atoms that no longer exist (when a
// liveness check is installed) are not written back.
func (a *Associations) Persist() error {
	if a == nil || a.dir == "" {
		return nil
	}
	if err := os.MkdirAll(a.dir, 0700); err != nil {
		return fmt.Errorf("associations: mkdir: %w", err)
	}
	unlock, err := flock.Lock(filepath.Join(a.dir, "associations.lock"))
	if err != nil {
		return fmt.Errorf("associations: lock: %w", err)
	}
	defer unlock()

	a.mu.Lock()
	defer a.mu.Unlock()

	disk := a.readDisk()
	merged := make(map[string]map[string]struct{})
	add := func(from, to string) {
		if merged[from] == nil {
			merged[from] = make(map[string]struct{})
		}
		merged[from][to] = struct{}{}
	}
	for from, list := range disk {
		if _, ok := a.removed[from]; ok {
			continue
		}
		for _, to := range list {
			if _, ok := a.removed[to]; ok {
				continue
			}
			add(from, to)
			add(to, from)
		}
	}
	alive := map[string]bool{}
	live := func(id string) bool {
		if a.exists == nil {
			return true
		}
		v, ok := alive[id]
		if !ok {
			v = a.exists(id)
			alive[id] = v
		}
		return v
	}
	for from, related := range a.links {
		for to := range related {
			if _, ok := merged[from][to]; ok {
				continue
			}
			// Not on disk: either new here, or removed by another process.
			if !live(from) || !live(to) {
				continue
			}
			add(from, to)
			add(to, from)
		}
	}
	// Adopt the merged view so later persists start from the shared state.
	a.links = merged
	data := make(map[string][]string, len(merged))
	for id, related := range merged {
		if session.ValidateSessionID(id) != nil {
			continue
		}
		list := make([]string, 0, len(related))
		for toID := range related {
			if session.ValidateSessionID(toID) != nil {
				continue
			}
			list = append(list, toID)
		}
		if len(list) > 0 {
			sort.Strings(list)
			data[id] = list
		}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("associations: marshal: %w", err)
	}
	file := filepath.Join(a.dir, "associations.json")
	if err := fsatomic.WriteFile(file, raw, 0600); err != nil {
		return fmt.Errorf("associations: write: %w", err)
	}
	return nil
}

// readDisk returns the persisted links, or nil when the file is absent or
// unreadable (the merge then writes this process's view alone).
func (a *Associations) readDisk() map[string][]string {
	raw, err := os.ReadFile(filepath.Join(a.dir, "associations.json"))
	if err != nil || len(raw) == 0 {
		return nil
	}
	var m map[string][]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// Load reads the association map from disk.
func (a *Associations) Load() error {
	if a == nil || a.dir == "" {
		return nil
	}
	file := filepath.Join(a.dir, "associations.json")
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("associations: read: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("associations: parse: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, list := range raw {
		if session.ValidateSessionID(id) != nil {
			continue
		}
		for _, toID := range list {
			if session.ValidateSessionID(toID) != nil {
				continue
			}
			a.linkOne(id, toID)
			a.linkOne(toID, id)
		}
	}
	return nil
}

// SetExists installs the liveness check used to drop links to atoms that no
// longer exist in the store.
func (a *Associations) SetExists(fn func(id string) bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.exists = fn
	a.mu.Unlock()
}

// Restore clears the removal marks of ids that were put back in the store, so
// their persisted links survive later merges.
func (a *Associations) Restore(ids ...string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		delete(a.removed, id)
	}
}
