package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/session"
)

// Quarantine stores tainted atoms separately from the live atom corpus. They
// count toward the overall size cap but are excluded from recall until a human
// promotes them.
type Quarantine struct {
	file    string
	mu      sync.RWMutex
	ttlDays int
}

// NewQuarantine creates a Quarantine store rooted at dir.
func NewQuarantine(dir string) *Quarantine {
	return &Quarantine{file: filepath.Join(dir, "quarantine.json")}
}

// SetTTLDays configures the TTL used when evicting expired entries at load
// time. Extended Memory calls this during construction.
func (q *Quarantine) SetTTLDays(days int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ttlDays = days
}

// quarantineEntry is a persisted tainted atom.
type quarantineEntry struct {
	MemoryAtom
	QuarantinedAt time.Time `json:"quarantined_at"`
}

// Store persists a tainted atom in quarantine.
func (q *Quarantine) Store(atom MemoryAtom) error {
	if atom.ID == "" {
		return fmt.Errorf("extended quarantine: atom id required")
	}
	if err := session.ValidateSessionID(atom.ID); err != nil {
		return fmt.Errorf("extended quarantine: invalid atom id: %w", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	entries, err := q.loadLocked()
	if err != nil {
		return err
	}
	entry := quarantineEntry{
		MemoryAtom:    atom,
		QuarantinedAt: time.Now().UTC(),
	}
	replaced := false
	for i, e := range entries {
		if e.ID == atom.ID {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return q.saveLocked(entries)
}

// List returns all quarantined atoms (newest first).
func (q *Quarantine) List() ([]MemoryAtom, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	entries, err := q.loadLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].QuarantinedAt.After(entries[j].QuarantinedAt)
	})
	atoms := make([]MemoryAtom, len(entries))
	for i, e := range entries {
		atoms[i] = e.MemoryAtom
	}
	return atoms, nil
}

// EvictExpired removes quarantined atoms older than ttlDays, returning the
// number removed. ttlDays <= 0 disables expiration.
func (q *Quarantine) EvictExpired(ttlDays int) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	entries, err := q.loadAtomsLocked()
	if err != nil {
		return 0, err
	}
	return q.evictExpiredLocked(ttlDays, entries)
}

// Size returns the on-disk size of quarantine.json in bytes.
func (q *Quarantine) Size() (int64, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	info, err := os.Stat(q.file)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("extended quarantine: stat: %w", err)
	}
	return info.Size(), nil
}

// Promote moves an atom from quarantine into a MemoryAtom. It does NOT remove
// the atom from quarantine; callers must call Forget after promoting if they
// want it removed from quarantine.
func (q *Quarantine) Promote(id string) (MemoryAtom, error) {
	if err := session.ValidateSessionID(id); err != nil {
		return MemoryAtom{}, fmt.Errorf("extended quarantine: invalid atom id: %w", err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	entries, err := q.loadLocked()
	if err != nil {
		return MemoryAtom{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e.MemoryAtom, nil
		}
	}
	return MemoryAtom{}, fmt.Errorf("extended quarantine: atom %s not found", id)
}

// Forget removes a quarantined atom by ID.
func (q *Quarantine) Forget(id string) error {
	if err := session.ValidateSessionID(id); err != nil {
		return fmt.Errorf("extended quarantine: invalid atom id: %w", err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	entries, err := q.loadLocked()
	if err != nil {
		return err
	}
	filtered := make([]quarantineEntry, 0, len(entries))
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, e)
	}
	if !found {
		return fmt.Errorf("extended quarantine: atom %s not found", id)
	}
	return q.saveLocked(filtered)
}

func (q *Quarantine) loadLocked() ([]quarantineEntry, error) {
	data, err := os.ReadFile(q.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("extended quarantine: read: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var entries []quarantineEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("extended quarantine: parse: %w", err)
	}
	if q.ttlDays > 0 {
		if removed, err := q.evictExpiredLocked(q.ttlDays, entries); err == nil && removed > 0 {
			return q.loadAtomsLocked()
		}
	}
	return entries, nil
}

// evictExpiredLocked is the inner implementation of EvictExpired. It filters
// entries in place without calling loadLocked again.
func (q *Quarantine) evictExpiredLocked(ttlDays int, entries []quarantineEntry) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -ttlDays)
	kept := make([]quarantineEntry, 0, len(entries))
	removed := 0
	for _, e := range entries {
		if e.QuarantinedAt.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed == 0 {
		return 0, nil
	}
	if err := q.saveLocked(kept); err != nil {
		return 0, err
	}
	return removed, nil
}

// loadAtomsLocked is a raw load used after evicting expired entries so the
// call does not recurse.
func (q *Quarantine) loadAtomsLocked() ([]quarantineEntry, error) {
	data, err := os.ReadFile(q.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("extended quarantine: read: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var entries []quarantineEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("extended quarantine: parse: %w", err)
	}
	return entries, nil
}

func (q *Quarantine) saveLocked(entries []quarantineEntry) error {
	if err := os.MkdirAll(filepath.Dir(q.file), 0700); err != nil {
		return fmt.Errorf("extended quarantine: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("extended quarantine: marshal: %w", err)
	}
	if err := fsatomic.WriteFile(q.file, data, 0600); err != nil {
		return fmt.Errorf("extended quarantine: write: %w", err)
	}
	return nil
}
