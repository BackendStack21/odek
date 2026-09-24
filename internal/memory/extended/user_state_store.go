package extended

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/fsatomic"
)

const userStateFileName = "user_model.json"

// userStateLockSuffix marks the advisory lock guarding cross-process
// read-modify-write of the user model file.
const userStateLockSuffix = ".lock"

// UserStateStore persists UserState atomically to disk.
type UserStateStore struct {
	mu   sync.Mutex
	file string
}

// NewUserStateStore creates a UserStateStore rooted at dir.
func NewUserStateStore(dir string) *UserStateStore {
	return &UserStateStore{file: filepath.Join(dir, userStateFileName)}
}

// Load reads the persisted UserState. Missing or empty files return a zero state.
func (s *UserStateStore) Load() (UserState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return UserState{}, nil
		}
		return UserState{}, fmt.Errorf("user state store: read: %w", err)
	}
	if len(data) == 0 {
		return UserState{}, nil
	}
	var state UserState
	if err := json.Unmarshal(data, &state); err != nil {
		return UserState{}, fmt.Errorf("user state store: parse: %w", err)
	}
	return state, nil
}

// Save writes the UserState atomically with restricted permissions.
func (s *UserStateStore) Save(state UserState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.file), 0700); err != nil {
		return fmt.Errorf("user state store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("user state store: marshal: %w", err)
	}
	if err := fsatomic.WriteFile(s.file, data, 0600); err != nil {
		return fmt.Errorf("user state store: write: %w", err)
	}
	return nil
}

// WithLock acquires an advisory cross-process lock on the user model file so
// that separate processes (serve + CLI) serialize read-modify-write cycles.
// The returned unlock function is safe to call more than once.
func (s *UserStateStore) WithLock() (func(), error) {
	lockPath := s.file + userStateLockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("user state store: lock mkdir: %w", err)
	}
	unlock, err := flock.Lock(lockPath)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(unlock) }, nil
}
