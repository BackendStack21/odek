package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/session"
)

// Associations stores bidirectional links between related atoms.
type Associations struct {
	mu    sync.RWMutex
	dir   string
	links map[string]map[string]struct{}
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
	delete(a.links, id)
	for fromID, related := range a.links {
		delete(related, id)
		if len(related) == 0 {
			delete(a.links, fromID)
		}
	}
}

// Persist saves the association map to disk.
func (a *Associations) Persist() error {
	if a == nil || a.dir == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := os.MkdirAll(a.dir, 0700); err != nil {
		return fmt.Errorf("associations: mkdir: %w", err)
	}
	data := make(map[string][]string, len(a.links))
	for id, related := range a.links {
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
