package events

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// collect returns a thread-safe event collector and its accessor.
func collect() (func(Event), func() []Event) {
	var mu sync.Mutex
	var got []Event
	return func(ev Event) {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}, func() []Event {
			mu.Lock()
			defer mu.Unlock()
			return append([]Event(nil), got...)
		}
}

func TestEmitter_StampsAndOrders(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "run-abc")
	em.SetSessionID("sess-1")

	types := []string{TypeRunStarted, TypeToolCallStarted, TypeToolCallCompleted, TypeRunCompleted}
	for _, ty := range types {
		em.Emit(Event{Type: ty})
	}
	em.Close()

	evs := got()
	if len(evs) != len(types) {
		t.Fatalf("got %d events, want %d", len(evs), len(types))
	}
	for i, ev := range evs {
		if ev.Type != types[i] {
			t.Errorf("event %d type = %q, want %q (order must be preserved)", i, ev.Type, types[i])
		}
		if ev.Schema != Schema {
			t.Errorf("event %d schema = %q, want %q", i, ev.Schema, Schema)
		}
		if ev.RunID != "run-abc" {
			t.Errorf("event %d run_id = %q, want run-abc", i, ev.RunID)
		}
		if ev.SessionID != "sess-1" {
			t.Errorf("event %d session_id = %q, want sess-1", i, ev.SessionID)
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("event %d missing timestamp", i)
		}
	}
}

func TestEmitter_SessionIDOnlyOnceKnown(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "run-xyz")
	em.Emit(Event{Type: TypeRunStarted})
	em.SetSessionID("sess-late")
	em.Emit(Event{Type: TypeSessionSaved})
	em.Close()

	evs := got()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	if evs[0].SessionID != "" {
		t.Errorf("early event session_id = %q, want empty (not yet known)", evs[0].SessionID)
	}
	if evs[1].SessionID != "sess-late" {
		t.Errorf("later event session_id = %q, want sess-late", evs[1].SessionID)
	}
}

func TestEmitter_PanickingHandlerDoesNotCrash(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	em := NewEmitter(func(ev Event) {
		if ev.Type == TypeToolCallStarted {
			panic("handler boom")
		}
		mu.Lock()
		seen = append(seen, ev.Type)
		mu.Unlock()
	}, "run-panic")

	em.Emit(Event{Type: TypeRunStarted})
	em.Emit(Event{Type: TypeToolCallStarted}) // panics inside dispatch
	em.Emit(Event{Type: TypeRunCompleted})    // must still be delivered
	em.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != TypeRunStarted || seen[1] != TypeRunCompleted {
		t.Errorf("delivered = %v, want [%s %s]", seen, TypeRunStarted, TypeRunCompleted)
	}
}

func TestEmitter_SlowHandlerNeverBlocksEmit(t *testing.T) {
	release := make(chan struct{})
	em := NewEmitter(func(Event) { <-release }, "run-slow")

	// Flood well past the buffer; Emit must return immediately every time.
	done := make(chan struct{})
	go func() {
		for i := 0; i < DefaultBufferSize*4; i++ {
			em.Emit(Event{Type: TypeIterationCompleted})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on a slow handler")
	}
	if em.Dropped() == 0 {
		t.Error("expected drop counter to increase when the queue is full")
	}
	close(release)
	em.Close()
}

func TestEmitter_EmitAfterCloseIsNoOp(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "run-x")
	em.Emit(Event{Type: TypeRunStarted})
	em.Close()
	em.Close() // idempotent
	em.Emit(Event{Type: TypeRunCompleted})
	if n := len(got()); n != 1 {
		t.Errorf("got %d events after close, want 1", n)
	}
}

func TestEmitter_RedactsHumanReadableFields(t *testing.T) {
	handler, got := collect()
	em := NewEmitter(handler, "run-redact")
	em.Emit(Event{
		Type: TypeToolCallFailed,
		Tool: "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Data: map[string]any{
			"note":  "key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked",
			"count": 3, // non-string values pass through untouched
		},
	})
	em.Close()

	evs := got()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if strings.Contains(evs[0].Tool, "sk-ant-") {
		t.Errorf("tool field was not redacted: %q", evs[0].Tool)
	}
	if s, _ := evs[0].Data["note"].(string); strings.Contains(s, "sk-ant-") {
		t.Errorf("data string was not redacted: %q", s)
	}
	if evs[0].Data["count"] != 3 {
		t.Errorf("non-string data value changed: %v", evs[0].Data["count"])
	}
}

func TestArgsDigest_StableAndDistinct(t *testing.T) {
	a := ArgsDigest(`{"text":"hello"}`)
	if a != ArgsDigest(`{"text":"hello"}`) {
		t.Error("digest not stable for identical args")
	}
	if a == ArgsDigest(`{"text":"bye"}`) {
		t.Error("digest collision for different args")
	}
	if len(a) != 64 {
		t.Errorf("digest length = %d, want 64 hex chars", len(a))
	}
}

func TestErrorClass(t *testing.T) {
	if got := ErrorClass(context.Canceled); got != "context_canceled" {
		t.Errorf("ErrorClass(context.Canceled) = %q", got)
	}
	if got := ErrorClass(context.DeadlineExceeded); got != "deadline_exceeded" {
		t.Errorf("ErrorClass(context.DeadlineExceeded) = %q", got)
	}
	if got := ErrorClass(errors.New("boom")); got != "error" {
		t.Errorf("ErrorClass(generic) = %q", got)
	}
	if got := ErrorClass(nil); got != "" {
		t.Errorf("ErrorClass(nil) = %q, want empty", got)
	}
}

func TestNewRunID(t *testing.T) {
	a, b := NewRunID(), NewRunID()
	if a == b {
		t.Error("run IDs collide")
	}
	if len(a) != 32 {
		t.Errorf("run ID length = %d, want 32 hex chars", len(a))
	}
}

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sink file: %v", err)
	}
	var evs []Event
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not parseable JSON: %v", i+1, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestJSONLSink_WritesParseableLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	sink, err := OpenJSONLSink(path)
	if err != nil {
		t.Fatalf("OpenJSONLSink: %v", err)
	}
	em := NewEmitter(func(ev Event) {
		if err := sink.Write(ev); err != nil {
			t.Errorf("sink write: %v", err)
		}
	}, NewRunID())
	em.Emit(Event{Type: TypeRunStarted, Data: map[string]any{"model": "test-model"}})
	em.Emit(Event{Type: TypeToolCallStarted, Tool: "echo", Iteration: 1,
		Data: map[string]any{"args_sha256": ArgsDigest(`{"text":"hello"}`), "args_bytes": 17}})
	em.Emit(Event{Type: TypeRunCompleted, Data: map[string]any{"duration_ms": 12}})
	em.Close()
	if err := sink.Close(); err != nil {
		t.Fatalf("sink close: %v", err)
	}

	evs := readEvents(t, path)
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	for i, ev := range evs {
		if ev.Schema != Schema {
			t.Errorf("event %d schema = %q", i, ev.Schema)
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("event %d missing timestamp", i)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestJSONLSink_Appends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	s1, err := OpenJSONLSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Write(Event{Type: TypeRunStarted}); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2, err := OpenJSONLSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Write(Event{Type: TypeRunCompleted}); err != nil {
		t.Fatal(err)
	}
	s2.Close()

	if evs := readEvents(t, path); len(evs) != 2 {
		t.Fatalf("got %d events after reopen, want 2 (append-only)", len(evs))
	}
}

func TestJSONLSink_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSONLSink(link); err == nil {
		t.Fatal("expected symlink refusal, got nil error")
	}
}

func TestJSONLSink_ParentDirMustExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "events.jsonl")
	if _, err := OpenJSONLSink(path); err == nil {
		t.Fatal("expected error for missing parent directory, got nil")
	}
}

func TestJSONLSink_HardensExistingFilePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sink, err := OpenJSONLSink(path)
	if err != nil {
		t.Fatal(err)
	}
	sink.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("existing file mode = %o, want hardened to 600", perm)
	}
}
