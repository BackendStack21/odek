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

// defaultFlushInterval bounds how long an acknowledged event may remain
// un-fsynced. Durability trade-off: with group commit a crash can lose up
// to ~defaultFlushInterval of the most recent events (the data is in the
// page cache and visible to readers; only an OS-level crash can lose it).
// This replaces one fsync per event, which capped throughput at ~1/fsync
// and made bursty event storms drop events when the dispatch buffer filled.
const defaultFlushInterval = 50 * time.Millisecond

// JSONLSink is an append-only sink that writes one JSON object per line.
//
// Safety properties:
//   - the parent directory must already exist (the sink never creates it)
//   - an existing symlink at the target path is refused
//   - the file is created (and hardened) with 0600 permissions
//   - every event is written to the file before Write returns
//   - durability is batched: fsync runs on a background flush timer
//     (defaultFlushInterval) and once more on Close, so a process crash
//     loses at most one flush interval of events; an OS crash can lose
//     the same window. Call Flush for a synchronous barrier.
type JSONLSink struct {
	mu            sync.Mutex
	f             *os.File
	syncFn        func() error // overridable in tests; defaults to f.Sync
	flushInterval time.Duration

	dirty     bool
	syncing   bool
	closed    bool
	flushCh   chan struct{} // closed by Close to stop the flusher
	flushDone chan struct{} // closed by the flusher goroutine on exit
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
	return &JSONLSink{f: f, syncFn: f.Sync, flushInterval: defaultFlushInterval}, nil
}

// Write appends one event as a single JSON line. The line is written to the
// file before Write returns; fsync is batched by the background flusher —
// see the durability note on JSONLSink. Safe for concurrent use.
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
	if s.closed {
		return os.ErrClosed
	}
	if _, err := s.f.Write(line); err != nil {
		return err
	}
	s.dirty = true
	s.ensureFlusherLocked()
	return nil
}

// ensureFlusherLocked lazily starts the background flush timer on the first
// write. Caller must hold s.mu.
func (s *JSONLSink) ensureFlusherLocked() {
	if s.flushCh != nil {
		return
	}
	s.flushCh = make(chan struct{})
	s.flushDone = make(chan struct{})
	interval := s.flushInterval
	if interval <= 0 {
		interval = defaultFlushInterval
	}
	go func() {
		defer close(s.flushDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.syncOnce()
			case <-s.flushCh:
				return
			}
		}
	}()
}

// syncOnce flushes the kernel page-cache state of the file if a write is
// pending and no sync is already in flight. Writes that land while the sync
// runs re-mark the sink dirty, so the next tick commits them.
func (s *JSONLSink) syncOnce() {
	s.mu.Lock()
	if !s.dirty || s.syncing || s.closed {
		s.mu.Unlock()
		return
	}
	s.syncing = true
	fn := s.syncFn
	s.mu.Unlock()

	err := fn()

	s.mu.Lock()
	s.syncing = false
	if err == nil {
		s.dirty = false
	}
	s.mu.Unlock()
}

// Flush synchronously fsyncs any pending writes. Use when a durability
// barrier is required before continuing.
func (s *JSONLSink) Flush() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return os.ErrClosed
	}
	fn := s.syncFn
	s.mu.Unlock()
	return fn()
}

// Close stops the flusher, flushes any pending writes, and closes the
// underlying file.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	flushCh := s.flushCh
	fn := s.syncFn
	dirty := s.dirty
	s.mu.Unlock()

	if flushCh != nil {
		close(flushCh)
		<-s.flushDone
	}
	var syncErr error
	if dirty && fn != nil {
		syncErr = fn()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.f.Close(); err != nil {
		return err
	}
	return syncErr
}
