package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// JSONLSink is an append-only sink that writes one JSON object per line.
//
// Safety properties:
//   - the parent directory must already exist (the sink never creates it)
//   - an existing symlink at the target path is refused
//   - the file is created (and hardened) with 0600 permissions
//   - every event is flushed to stable storage before Write returns
type JSONLSink struct {
	mu sync.Mutex
	f  *os.File
}

// OpenJSONLSink opens path for append-only event writes, creating it with
// 0600 permissions if necessary. The parent directory must already exist.
func OpenJSONLSink(path string) (*JSONLSink, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("parent directory %s must already exist: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("parent path %s is not a directory", dir)
	}
	// Refuse to follow a symlink at the target path — an attacker who can
	// plant a symlink could otherwise redirect the event stream (which may
	// contain session IDs and token counts) over an arbitrary file. The
	// Lstat pre-check rejects an existing symlink; O_NOFOLLOW closes the
	// check-then-open race where the symlink is swapped in between.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write events to symlink %s", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	// Harden a pre-existing file that was created with looser permissions.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return &JSONLSink{f: f}, nil
}

// Write appends one event as a single JSON line and flushes it to stable
// storage. Safe for concurrent use.
func (s *JSONLSink) Write(ev Event) error {
	if ev.Schema == "" {
		ev.Schema = Schema
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.f.Write(line); err != nil {
		return err
	}
	return s.f.Sync()
}

// Close flushes and closes the underlying file.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
