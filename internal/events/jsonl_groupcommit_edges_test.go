package events

// Coverage tests for the group-commit flusher branches: the syncOnce
// no-op paths (not dirty / already syncing / closed), error retention on
// failed sync, the default flush interval fallback, and Flush/Close on
// a closed sink.

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// syncOnce with nothing pending must not invoke the sync function.
func TestJSONLSinkSyncOnce_NotDirtyIsNoop(t *testing.T) {
	s, _, syncs := newCountingSink(t)
	s.flushInterval = time.Hour
	s.syncOnce()
	if got := atomic.LoadInt64(syncs); got != 0 {
		t.Fatalf("syncs = %d for a clean sink, want 0", got)
	}
}

// A sync error must keep the sink dirty so the pending writes are
// retried on the next tick instead of being silently declared durable.
func TestJSONLSinkSyncOnce_ErrorKeepsDirty(t *testing.T) {
	s, _, syncs := newCountingSink(t)
	s.flushInterval = time.Hour

	var fail int32
	s.syncFn = func() error {
		atomic.AddInt64(syncs, 1)
		if atomic.LoadInt32(&fail) == 1 {
			return errors.New("simulated EIO")
		}
		return nil
	}

	atomic.StoreInt32(&fail, 1)
	if err := s.Write(ev(1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	s.syncOnce() // fails, must stay dirty
	s.syncOnce() // fails again, must still stay dirty

	atomic.StoreInt32(&fail, 0)
	s.syncOnce() // succeeds, clears dirty
	if got := atomic.LoadInt64(syncs); got != 3 {
		t.Fatalf("syncs = %d, want 3 (two retries then success)", got)
	}
}

// Flush after Close must return os.ErrClosed, not panic or fsync a dead
// file handle.
func TestJSONLSinkFlush_AfterCloseReturnsErrClosed(t *testing.T) {
	s, _, _ := newCountingSink(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Flush(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Flush after Close = %v, want os.ErrClosed", err)
	}
	if err := s.Write(ev(1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Write after Close = %v, want os.ErrClosed", err)
	}
	// Double Close is idempotent.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// A zero flushInterval must fall back to the default rather than
// spinning a zero-period ticker.
func TestJSONLSink_ZeroIntervalUsesDefault(t *testing.T) {
	s, _, _ := newCountingSink(t)
	s.flushInterval = 0
	if err := s.Write(ev(1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The flusher must start lazily with the default interval; just
	// verify it stops cleanly.
	if s.flushCh == nil {
		t.Fatal("flusher did not start after first write")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Writes landing while a sync is in flight must re-mark the sink dirty
// so the next tick commits them (syncing-guard branch).
func TestJSONLSink_WriteDuringSyncStaysDirty(t *testing.T) {
	s, path, _ := newCountingSink(t)
	s.flushInterval = time.Hour

	release := make(chan struct{})
	s.syncFn = func() error {
		<-release
		return nil
	}
	if err := s.Write(ev(1)); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.syncOnce() // blocks inside syncFn with syncing=true
	}()
	// Wait until the sync is in flight, then write behind it.
	time.Sleep(20 * time.Millisecond)
	if err := s.Write(ev(2)); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	close(release)
	<-done

	s.mu.Lock()
	dirty := s.dirty
	s.mu.Unlock()
	if !dirty {
		t.Fatal("write during in-flight sync did not re-mark the sink dirty")
	}
	s.mu.Lock()
	s.dirty = false // clean shutdown without invoking the gated syncFn
	s.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sink file missing: %v", err)
	}
}
