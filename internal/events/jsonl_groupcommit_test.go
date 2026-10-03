package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newCountingSink opens a JSONLSink whose Sync calls are counted. The
// counter stands in for real fsyncs so the group-commit batching can be
// asserted without a filesystem that reports fsync cost.
func newCountingSink(t *testing.T) (*JSONLSink, string, *int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	s, err := OpenJSONLSink(path)
	if err != nil {
		t.Fatalf("OpenJSONLSink: %v", err)
	}
	var syncs int64
	s.syncFn = func() error {
		atomic.AddInt64(&syncs, 1)
		return nil
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path, &syncs
}

func ev(n int) Event {
	return Event{Schema: Schema, Type: "run_started", RunID: "r", SessionID: "s", Data: map[string]any{"n": n}}
}

// Group commit must coalesce bursts: writing many events inside one flush
// interval must cost far fewer than one sync per event.
func TestJSONLSinkGroupCommit_BatchesSyncs(t *testing.T) {
	s, _, syncs := newCountingSink(t)
	s.flushInterval = 20 * time.Millisecond

	for i := 0; i < 50; i++ {
		if err := s.Write(ev(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	// Allow two flush ticks so the burst is fully committed.
	time.Sleep(150 * time.Millisecond)
	got := atomic.LoadInt64(syncs)
	if got >= 50 {
		t.Fatalf("syncs = %d for a 50-event burst; group commit not batching", got)
	}
	if got == 0 {
		t.Fatalf("no syncs observed after flush interval")
	}
}

// Close must flush everything still pending, so no acknowledged write is
// lost even when the process exits right after.
func TestJSONLSinkGroupCommit_CloseFlushesPending(t *testing.T) {
	s, path, _ := newCountingSink(t)
	s.flushInterval = time.Hour // nothing should flush on the timer

	if err := s.Write(ev(1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), `"run_started"`) {
		t.Fatalf("pending event lost on Close: %q", data)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
		t.Fatalf("lines = %d, want 1", lines)
	}
}

// Every write must still be atomically a single JSON line — grouping must
// never interleave or tear lines.
func TestJSONLSinkGroupCommit_LinesRemainIntact(t *testing.T) {
	s, path, _ := newCountingSink(t)
	s.flushInterval = 10 * time.Millisecond

	for i := 0; i < 25; i++ {
		if err := s.Write(ev(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", i, err, line)
		}
	}
}
