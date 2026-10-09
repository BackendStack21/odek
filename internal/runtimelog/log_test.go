package runtimelog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/flock"
)

func records(t *testing.T, path string) []Record {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		var ev Record
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestMetadataBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := OpenWithOptions(Options{Path: path, Surface: "test", MaxFileMB: 50, MaxFiles: 4, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	l.Emit(events.Event{Type: "tool_call_started", SessionID: "session-1", TaskID: "child", Data: map[string]any{
		"call_id": "call-1", "args": "private arguments", "args_summary": map[string]any{"path": "private path"}, "goal": "private goal", "result": "private result", "reason": "private denial", "duration_ms": map[string]any{"secret": "nested"},
	}})
	l.Emit(events.Event{Type: "unknown", Data: map[string]any{"model": "private unknown"}})
	l.Close()
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "nested") {
		t.Fatalf("content leaked: %s", b)
	}
	evs := records(t, path)
	if len(evs) != 1 || evs[0].SessionID != "session-1" || evs[0].TaskID != "child" {
		t.Fatalf("records: %+v", evs)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
func TestSlowSinkDoesNotBlockProducerAndReportsDrops(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &Logger{queue: make(chan []byte, 2), done: make(chan struct{}), abort: make(chan struct{})}
	var once sync.Once
	l.write = func([]byte) error { once.Do(func() { close(entered) }); <-release; return nil }
	go l.run()
	l.Emit(events.Event{Type: "run_started"})
	<-entered
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			l.Emit(events.Event{Type: "run_started"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer blocked")
	}
	if l.Dropped() == 0 {
		t.Fatal("expected loss accounting")
	}
	close(release)
	l.Close()
}
func TestContentionRetainsBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := Open(path, "test", 50)
	if err != nil {
		t.Fatal(err)
	}
	release, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Emit(events.Event{Type: "run_completed", SessionID: "retained"})
	time.Sleep(350 * time.Millisecond) // exceed one lock attempt, as a janitor scan can
	release()
	l.Close()
	if l.failures.Load() != 0 || len(records(t, path)) != 1 {
		t.Fatal("ordinary lock contention lost the batch")
	}
}
func TestSymlinksRejected(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	_ = os.WriteFile(victim, []byte("keep"), 0600)
	path := filepath.Join(dir, "runtime.log")
	_ = os.Symlink(victim, path)
	if l, err := Open(path, "test", 50); err == nil {
		l.Close()
		t.Fatal("accepted log symlink")
	}
	_ = os.Remove(path)
	_ = os.Symlink(victim, path+".lock")
	if _, err := lock(path); err == nil {
		t.Fatal("accepted lock symlink")
	}
	b, _ := os.ReadFile(victim)
	if string(b) != "keep" {
		t.Fatal("modified symlink target")
	}
}
func TestPruneRetentionAndPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now()
	write := func(name string) {
		a, _ := json.Marshal(events.Event{Type: "run_started", Timestamp: old, SessionID: "expired"})
		b, _ := json.Marshal(events.Event{Type: "run_started", Timestamp: recent, SessionID: "keep"})
		if err := os.WriteFile(name, []byte(string(a)+"\n"+string(b)+"\n{malformed}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(path)
	write(path + ".1")
	before, _ := os.ReadFile(path)
	cutoff := time.Now().Add(-24 * time.Hour)
	if n, err := Prune(context.Background(), path, cutoff, true); err != nil || n != 2 {
		t.Fatalf("preview %d %v", n, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("preview mutated log")
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("preview created lock")
	}
	if n, err := Prune(context.Background(), path, cutoff, false); err != nil || n != 2 {
		t.Fatalf("prune %d %v", n, err)
	}
	for _, name := range []string{path, path + ".1"} {
		b, _ := os.ReadFile(name)
		if strings.Contains(string(b), "expired") || !strings.Contains(string(b), "keep") || !strings.Contains(string(b), "malformed") {
			t.Fatalf("unexpected retention: %s", b)
		}
	}
}

// An oversized line is one malformed record: Prune neither fails nor rewrites
// a file that has nothing expired.
func TestPruneOversizedOnlyRecordPreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	raw := strings.Repeat("x", 2<<20)
	_ = os.WriteFile(path, []byte(raw), 0600)
	if n, err := Prune(context.Background(), path, time.Now(), false); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != raw {
		t.Fatal("prune replaced a log with nothing expired")
	}
}

// This test invokes a second copy of the test executable to exercise kernel
// locking between processes, rather than relying on an in-process mutex.
func TestMultiprocessWriter(t *testing.T) {
	if path := os.Getenv("ODEK_RUNTIME_LOG_TEST_PATH"); path != "" {
		l, err := Open(path, "helper", 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 150; i++ {
			l.Emit(events.Event{Type: "run_completed", SessionID: fmt.Sprintf("pid-%d-%d", os.Getpid(), i)})
		}
		l.Close()
		if l.failures.Load() != 0 || l.Dropped() != 0 {
			t.Fatal("helper lost records")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "runtime.log")
	var cmds []*exec.Cmd
	for i := 0; i < 3; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMultiprocessWriter$")
		cmd.Env = append(os.Environ(), "ODEK_RUNTIME_LOG_TEST_PATH="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(records(t, path)); n != 450 {
		t.Fatalf("records=%d", n)
	}
}
func TestConcurrentRotationPruningAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := Open(path, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			l.Emit(events.Event{Type: "run_completed", SessionID: fmt.Sprintf("session-%d", i)})
		}
	}()
	// One rotation preserves all records across current + backup; repeated
	// rotations intentionally evict older backups and cannot promise completeness.
	_, err = Rotate(path, 0)
	if err != nil && !errors.Is(err, flock.ErrLocked) {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := Prune(context.Background(), path, time.Now().Add(-time.Hour), false); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	l.Close()
	all := records(t, path)
	if _, err := os.Stat(path + ".1"); err == nil {
		all = append(all, records(t, path+".1")...)
	}
	if len(all) != 100 {
		t.Fatalf("retention/rotation lost appends: %d", len(all))
	}
}

func TestShutdownBoundedDuringBlockedSink(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := &Logger{queue: make(chan []byte, 2), done: make(chan struct{}), abort: make(chan struct{})}
	l.write = func([]byte) error { close(entered); <-release; return nil }
	go l.run()
	l.Emit(events.Event{Type: "run_started"})
	<-entered
	start := time.Now()
	l.Close()
	if time.Since(start) > 3*time.Second {
		t.Fatal("shutdown waited indefinitely")
	}
	close(release)
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
}
func TestWriteFailureReported(t *testing.T) {
	l := &Logger{queue: make(chan []byte, 2), done: make(chan struct{}), abort: make(chan struct{}), write: func([]byte) error { return errors.New("disk failure with private data") }}
	go l.run()
	l.Emit(events.Event{Type: "run_started"})
	l.Close()
	if l.failures.Load() != 1 {
		t.Fatal("write failure not counted")
	}
}
