package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/fsatomic"
	"github.com/BackendStack21/odek/internal/session"
)

// atomMeta is the on-disk metadata record for a trusted atom. The atom text
// lives in a separate chunk file so metadata updates (pin, context) do not
// rewrite the full atom.
type atomMeta struct {
	ID          string      `json:"id"`
	SourceClass string      `json:"source_class"`
	Type        string      `json:"type"`
	CreatedAt   time.Time   `json:"created_at"`
	Context     AtomContext `json:"context,omitempty"`
	Pin         bool        `json:"pin,omitempty"`
	Confidence  float32     `json:"confidence,omitempty"`
}

// AtomStore persists MemoryAtoms to disk. Atom text is stored in
// extended/chunks/<id>.md; metadata is kept in extended/atoms.json.
// Operations are serialized by a per-instance RWMutex; instances sharing a
// directory also coordinate via the per-directory lock returned by dirLock.
type AtomStore struct {
	dir       string
	chunksDir string
	atomsFile string
	mu        sync.RWMutex
}

// NewAtomStore creates an AtomStore rooted at dir (e.g. ~/.odek/memory/extended).
func NewAtomStore(dir string) *AtomStore {
	return &AtomStore{
		dir:       dir,
		chunksDir: filepath.Join(dir, "chunks"),
		atomsFile: filepath.Join(dir, "atoms.json"),
	}
}

// Add persists a new atom. The atom ID is validated for path safety and the
// text is capped to maxChars.
func (s *AtomStore) Add(atom MemoryAtom, maxChars int) error {
	if err := session.ValidateSessionID(atom.ID); err != nil {
		return fmt.Errorf("extended store: invalid atom id: %w", err)
	}
	if atom.Text == "" {
		return fmt.Errorf("extended store: empty text")
	}
	if maxChars > 0 && len(atom.Text) > maxChars {
		atom.Text = atom.Text[:maxChars]
		// Back off to the last rune boundary so truncation cannot split a
		// multi-byte UTF-8 character.
		for len(atom.Text) > 0 && !utf8.ValidString(atom.Text) {
			atom.Text = atom.Text[:len(atom.Text)-1]
		}
	}

	lock := dirLock(s.dir)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.chunksDir, 0700); err != nil {
		return fmt.Errorf("extended store: mkdir chunks: %w", err)
	}

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return err
	}

	// Write chunk file.
	chunkPath := s.chunkPath(atom.ID)
	if err := fsatomic.WriteFile(chunkPath, []byte(atom.Text), 0600); err != nil {
		return fmt.Errorf("extended store: write chunk: %w", err)
	}

	// Update or append metadata.
	meta := atomMeta{
		ID:          atom.ID,
		SourceClass: atom.SourceClass,
		Type:        atom.Type,
		CreatedAt:   atom.CreatedAt,
		Context:     atom.Context,
		Pin:         atom.Pin,
		Confidence:  atom.Confidence,
	}
	replaced := false
	for i, m := range metas {
		if m.ID == atom.ID {
			metas[i] = meta
			replaced = true
			break
		}
	}
	if !replaced {
		metas = append(metas, meta)
	}

	if err := s.saveAtomsLocked(metas); err != nil {
		return err
	}
	return nil
}

// Get loads an atom by ID.
func (s *AtomStore) Get(id string) (MemoryAtom, error) {
	if err := session.ValidateSessionID(id); err != nil {
		return MemoryAtom{}, fmt.Errorf("extended store: invalid atom id: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return MemoryAtom{}, err
	}
	var meta atomMeta
	found := false
	for _, m := range metas {
		if m.ID == id {
			meta = m
			found = true
			break
		}
	}
	if !found {
		return MemoryAtom{}, fmt.Errorf("extended store: atom %s not found", id)
	}

	text, err := os.ReadFile(s.chunkPath(id))
	if err != nil {
		return MemoryAtom{}, fmt.Errorf("extended store: read chunk %s: %w", id, err)
	}

	return MemoryAtom{
		ID:          meta.ID,
		Text:        string(text),
		SourceClass: meta.SourceClass,
		Type:        meta.Type,
		CreatedAt:   meta.CreatedAt,
		Context:     meta.Context,
		Pin:         meta.Pin,
		Confidence:  meta.Confidence,
	}, nil
}

// Remove deletes an atom by ID.
func (s *AtomStore) Remove(id string) error {
	if err := session.ValidateSessionID(id); err != nil {
		return fmt.Errorf("extended store: invalid atom id: %w", err)
	}

	lock := dirLock(s.dir)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return err
	}
	filtered := make([]atomMeta, 0, len(metas))
	for _, m := range metas {
		if m.ID != id {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == len(metas) {
		return fmt.Errorf("extended store: atom %s not found", id)
	}

	if err := os.Remove(s.chunkPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("extended store: remove chunk %s: %w", id, err)
	}
	if err := s.saveAtomsLocked(filtered); err != nil {
		return err
	}
	return nil
}

// List returns all persisted atoms sorted by CreatedAt descending.
func (s *AtomStore) List() ([]MemoryAtom, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return nil, err
	}

	atoms := make([]MemoryAtom, 0, len(metas))
	for _, meta := range metas {
		text, err := os.ReadFile(s.chunkPath(meta.ID))
		if err != nil {
			continue
		}
		atoms = append(atoms, MemoryAtom{
			ID:          meta.ID,
			Text:        string(text),
			SourceClass: meta.SourceClass,
			Type:        meta.Type,
			CreatedAt:   meta.CreatedAt,
			Context:     meta.Context,
			Pin:         meta.Pin,
			Confidence:  meta.Confidence,
		})
	}
	sort.Slice(atoms, func(i, j int) bool {
		return atoms[i].CreatedAt.After(atoms[j].CreatedAt)
	})
	return atoms, nil
}

// Pin sets or clears the Pin flag on an atom.
func (s *AtomStore) Pin(id string, pin bool) error {
	if err := session.ValidateSessionID(id); err != nil {
		return fmt.Errorf("extended store: invalid atom id: %w", err)
	}

	lock := dirLock(s.dir)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return err
	}
	found := false
	for i, m := range metas {
		if m.ID == id {
			metas[i].Pin = pin
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("extended store: atom %s not found", id)
	}
	return s.saveAtomsLocked(metas)
}

// Size returns the total on-disk size of the trusted atom store in bytes
// (chunks + atoms.json).
func (s *AtomStore) Size() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sizeLocked()
}

// AtomSize returns the estimated on-disk bytes for a single atom: its chunk
// file plus its proportional share of atoms.json.
func (s *AtomStore) AtomSize(id string) (int64, error) {
	if err := session.ValidateSessionID(id); err != nil {
		return 0, fmt.Errorf("extended store: invalid atom id: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	metas, err := s.loadAtomsLocked()
	if err != nil {
		return 0, err
	}
	if len(metas) == 0 {
		return 0, fmt.Errorf("extended store: atom %s not found", id)
	}

	var meta atomMeta
	found := false
	for _, m := range metas {
		if m.ID == id {
			meta = m
			found = true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("extended store: atom %s not found", id)
	}

	info, err := os.Stat(s.chunkPath(meta.ID))
	if err != nil {
		return 0, fmt.Errorf("extended store: stat chunk %s: %w", id, err)
	}
	chunkSize := info.Size()

	atomsJSONSize, err := s.atomsJSONSizeLocked()
	if err != nil {
		return 0, err
	}
	share := atomsJSONSize / int64(len(metas))

	return chunkSize + share, nil
}

// Refresh reloads the store from disk. It is a no-op now that all reads go
// straight to disk, but it remains the extension point for future caching.
func (s *AtomStore) Refresh() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return nil
}

// loadAtomsLocked reads atoms.json. Caller must hold s.mu (read or write).
func (s *AtomStore) loadAtomsLocked() ([]atomMeta, error) {
	data, err := os.ReadFile(s.atomsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("extended store: read atoms.json: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var metas []atomMeta
	if err := json.Unmarshal(data, &metas); err != nil {
		return nil, fmt.Errorf("extended store: parse atoms.json: %w", err)
	}
	return metas, nil
}

// saveAtomsLocked writes atoms.json atomically. Caller must hold s.mu and the
// dirLock.
func (s *AtomStore) saveAtomsLocked(metas []atomMeta) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("extended store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(metas, "", "  ")
	if err != nil {
		return fmt.Errorf("extended store: marshal atoms.json: %w", err)
	}
	if err := fsatomic.WriteFile(s.atomsFile, data, 0600); err != nil {
		return fmt.Errorf("extended store: write atoms.json: %w", err)
	}
	return nil
}

// atomsJSONSizeLocked returns the size of atoms.json. Caller must hold s.mu.
func (s *AtomStore) atomsJSONSizeLocked() (int64, error) {
	info, err := os.Stat(s.atomsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("extended store: stat atoms.json: %w", err)
	}
	return info.Size(), nil
}

// sizeLocked returns total trusted store size. Caller must hold s.mu.
func (s *AtomStore) sizeLocked() (int64, error) {
	var total int64
	if err := filepath.Walk(s.chunksDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("extended store: walk chunks: %w", err)
	}
	size, err := s.atomsJSONSizeLocked()
	if err != nil {
		return 0, err
	}
	total += size
	return total, nil
}

func (s *AtomStore) chunkPath(id string) string {
	return filepath.Join(s.chunksDir, id+".md")
}

// dirLocks serializes mutations across AtomStore instances that share the same
// directory, mirroring the pattern used by the legacy fact store.
var (
	dirLocksMu sync.Mutex
	dirLocks   = map[string]*sync.Mutex{}
)

func dirLock(dir string) *sync.Mutex {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	dirLocksMu.Lock()
	defer dirLocksMu.Unlock()
	mu := dirLocks[abs]
	if mu == nil {
		mu = &sync.Mutex{}
		dirLocks[abs] = mu
	}
	return mu
}
