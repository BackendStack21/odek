package runtimelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/flock"
)

func TestLogLevelsAndInvalidRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := Open(path, "test", math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if l.maxBytes != math.MaxInt64 {
		t.Fatal("rotation size overflowed")
	}
	cases := []struct{ typ, status, level string }{
		{"operation_failed", "", "ERROR"}, {"operation_warning", "", "WARN"}, {"panic_recovered", "", "ERROR"},
		{"run_started", "", "INFO"}, {"run_failed", "", "ERROR"}, {"llm_call_failed", "", "ERROR"},
		{"subagent_failed", "", "ERROR"}, {"tool_call_failed", "", "WARN"}, {"subagent_denied", "", "WARN"},
		{"budget_warning", "", "WARN"}, {"tool_recovery", "", "WARN"}, {"logging_dropped", "", "WARN"},
		{"subagent_completed", "success", "INFO"}, {"subagent_completed", "completed", "INFO"},
		{"subagent_completed", "partial", "WARN"}, {"subagent_completed", "cancelled", "WARN"},
		{"subagent_completed", "timeout", "ERROR"}, {"subagent_completed", "", "ERROR"},
	}
	for _, tc := range cases {
		l.Emit(events.Event{Type: tc.typ, Data: map[string]any{"status": tc.status}})
	}
	l.Emit(events.Event{Type: "run_completed", Data: map[string]any{"cost_usd": math.NaN()}})
	if l.Dropped() != 1 {
		t.Fatal("non-finite JSON record was not rejected")
	}
	l.Close()
	l.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Emit(events.Event{Type: "run_started"})
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("Emit after Close changed log")
	}
	lines := strings.Split(strings.TrimSpace(string(before)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("records=%d want=%d", len(lines), len(cases))
	}
	for i, line := range lines {
		var rec struct{ Type, Level string }
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Type != cases[i].typ || rec.Level != cases[i].level {
			t.Fatalf("record %d: %+v", i, rec)
		}
	}
	// An oversized origin label cannot bypass the per-record byte bound.
	oversized := &Logger{queue: make(chan []byte, 1), surface: strings.Repeat("x", maxRecordBytes)}
	oversized.Emit(events.Event{Type: "run_started"})
	if oversized.Dropped() != 1 || len(oversized.queue) != 0 {
		t.Fatal("oversized record entered queue")
	}
}

func TestSanitizePreservesScalarMetadataOnly(t *testing.T) {
	stamp := time.Now().UTC().Truncate(time.Millisecond)
	input := events.Event{Type: "run_started", Timestamp: stamp, Tool: strings.Repeat("x", 200), SourceTaskID: "source", RunID: "run", TurnID: "turn", SessionID: "session", RootRunID: "root", ParentRunID: "parent", ParentTurnID: "parent-turn", TaskID: "task", ParentTaskID: "parent-task", Data: map[string]any{
		"model": "model", "pid": int32(10), "depth": int64(2), "count": uint(3), "dropped": uint64(4), "cost_usd": float32(0.5), "duration_ms": float64(12), "iterations": 7, "sandbox": true,
		"call_id": []string{"private"}, "profile": map[string]any{"private": "content"}, "input_tokens": "private", "goal": "private", "args_summary": "private",
	}}
	out, ok := Sanitize(input)
	if !ok {
		t.Fatal("known event rejected")
	}
	if len(out.Tool) != 128 || !out.Timestamp.Equal(stamp) || out.SourceTaskID != "source" || out.Schema != events.Schema {
		t.Fatalf("envelope damaged: %+v", out)
	}
	for _, key := range []string{"model", "pid", "depth", "count", "dropped", "cost_usd", "duration_ms", "iterations", "sandbox"} {
		if out.Data[key] != input.Data[key] {
			t.Fatalf("scalar %s lost", key)
		}
	}
	for _, key := range []string{"call_id", "profile", "input_tokens", "goal", "args_summary"} {
		if _, ok := out.Data[key]; ok {
			t.Fatalf("unsafe field survived: %s", key)
		}
	}
	out.Data["model"] = "changed"
	if input.Data["model"] != "model" {
		t.Fatal("sanitizer aliases caller map")
	}
	out, _ = Sanitize(events.Event{Type: "run_started", Data: map[string]any{"sandbox": "private"}})
	if _, ok := out.Data["sandbox"]; ok {
		t.Fatal("nonboolean sandbox retained")
	}
	key := "sk-" + strings.Repeat("a", 48)
	if strings.Contains(short(key), key) {
		t.Fatal("identifier secret not redacted")
	}
}

func TestFilesystemRejectionsPreserveTargets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(file, "runtime.log"), "test", 50); err == nil {
		t.Fatal("accepted nondirectory parent")
	}
	if _, err := openPrivate(os.DevNull); err == nil {
		t.Fatal("accepted device log")
	}
	path := filepath.Join(dir, "runtime.log")
	if rotated, err := Rotate(path, 0); err != nil || rotated {
		t.Fatalf("missing rotation: %v %v", rotated, err)
	}
	if rotated, err := rotateLocked(path, 0); err != nil || rotated {
		t.Fatalf("missing locked rotation: %v %v", rotated, err)
	}
	if _, err := rotateLocked(filepath.Join(file, "child"), 0); err == nil {
		t.Fatal("expected stat error")
	}
	if err := os.Symlink(file, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(path, 0); err == nil {
		t.Fatal("rotated symlink")
	}
	l := &Logger{path: path, maxBytes: 1}
	if err := l.append([]byte("replace")); err == nil {
		t.Fatal("append followed rotating symlink")
	}
	l.maxBytes = 0
	if err := l.append([]byte("replace")); err == nil {
		t.Fatal("append followed symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".1", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(path, 0); err == nil {
		t.Fatal("rotation replaced backup directory")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "original" {
		t.Fatal("failed rotation damaged current log")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(path, 0); err == nil {
		t.Fatal("accepted symlink lock")
	}
	b, _ = os.ReadFile(file)
	if string(b) != "keep" {
		t.Fatal("modified protected target")
	}
}

func TestRotationKeepsExactlyOneBackupAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := Open(path, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise synchronous writer batches deterministically, with its real lock.
	if err := l.append([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if changed, err := Rotate(path, 1); err != nil || !changed {
		t.Fatalf("first rotation: %v %v", changed, err)
	}
	if err := l.append([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if changed, err := Rotate(path, 1); err != nil || !changed {
		t.Fatalf("second rotation: %v %v", changed, err)
	}
	if err := l.append([]byte("third\n")); err != nil {
		t.Fatal(err)
	}
	l.Close()
	current, _ := os.ReadFile(path)
	backup, _ := os.ReadFile(path + ".1")
	if string(current) != "third\n" || string(backup) != "second\n" {
		t.Fatalf("stale descriptor or wrong backup: %q %q", current, backup)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatal("unexpected second backup")
	}
}

func TestShutdownAbortsPersistentContention(t *testing.T) {
	entered := make(chan struct{}, 1)
	l := &Logger{queue: make(chan []byte, 2), done: make(chan struct{}), abort: make(chan struct{})}
	l.write = func([]byte) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		return flock.ErrLocked
	}
	go l.run()
	l.Emit(events.Event{Type: "run_started"})
	<-entered
	l.Close()
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("contended writer leaked after shutdown")
	}
	if l.failures.Load() != 1 {
		t.Fatal("abandoned batch not reported")
	}
}

func TestIdleWriterReportsLossWithoutClosing(t *testing.T) {
	dir := t.TempDir()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() { os.Stderr = previous; _ = stderr.Close() })
	l, err := Open(filepath.Join(dir, "runtime.log"), "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	l.Emit(events.Event{Type: "run_completed", Data: map[string]any{"cost_usd": math.Inf(1)}})
	t.Cleanup(l.Close)
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(stderr.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "dropped=1") {
			break
		}
		if time.Now().After(deadline) {
			l.Close()
			t.Fatal("no periodic loss diagnostic")
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.Close()
}

func TestRetentionMissingAndBackupOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.log")
	for _, p := range []string{filepath.Join(dir, "missing", "runtime.log"), path} {
		if n, err := Prune(t.Context(), p, time.Now(), false); err != nil || n != 0 {
			t.Fatalf("missing path: %d %v", n, err)
		}
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("empty retention created lock")
	}
	b, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(path+".1", append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(t.Context(), path, time.Now(), false); err != nil || n != 1 {
		t.Fatalf("backup-only retention: %d %v", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("backup cleanup created current log")
	}
}

func TestRetentionUnsafeTargetsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.log")
	original := []byte(`{"timestamp":"2020-01-01T00:00:00Z"}` + "\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Prune(ctx, path, time.Now(), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !bytes.Equal(b, original) {
		t.Fatal("cancelled pruning committed changes")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".runtime-prune-*"))
	if len(matches) != 0 {
		t.Fatal("cancel left temporary files")
	}
	if err := os.Symlink(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(t.Context(), path, time.Now(), false); err == nil || n != 1 {
		t.Fatalf("expected partial commit before rejecting backup: n=%d err=%v", n, err)
	}
	b, _ = os.ReadFile(path)
	if len(b) != 0 {
		t.Fatal("successful first replacement rolled back")
	}
	if _, err := pruneFile(t.Context(), dir, time.Now(), true); err == nil {
		t.Fatal("accepted directory input")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(t.Context(), path, time.Now(), false); err == nil {
		t.Fatal("accepted symlink lock")
	}
}

func TestRetentionPreservesBoundaryFutureAndUndated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	cutoff := time.Now().UTC().Truncate(time.Second)
	var lines []string
	for _, stamp := range []time.Time{cutoff.Add(-time.Nanosecond), cutoff, cutoff.Add(time.Hour)} {
		b, _ := json.Marshal(events.Event{Timestamp: stamp})
		lines = append(lines, string(b))
	}
	lines = append(lines, `{"timestamp":"bad"}`, `{"type":"undated"}`, `{"timestamp":"0001-01-01T00:00:00Z"}`)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(t.Context(), path, cutoff, false); err != nil || n != 1 {
		t.Fatalf("strict cutoff: %d %v", n, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != strings.Join(lines[1:], "\n")+"\n" {
		t.Fatal("retention removed ineligible records")
	}
}

func TestRetentionTempCreationFailurePreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.log")
	raw := []byte(`{"timestamp":"2020-01-01T00:00:00Z"}` + "\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	probe, err := os.CreateTemp(dir, "permission-probe-*")
	if err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem or user bypasses directory write permissions")
	}
	// This direct helper invocation isolates temp creation from lock creation.
	if n, err := pruneFile(t.Context(), path, time.Now(), false); err == nil || n != 0 {
		t.Fatalf("temp creation unexpectedly succeeded: n=%d err=%v", n, err)
	}
	b, _ := os.ReadFile(path)
	if !bytes.Equal(b, raw) {
		t.Fatal("failed temp creation changed input")
	}
}
