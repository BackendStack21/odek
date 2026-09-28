// Package runtimelog writes bounded, metadata-only operational logs. Producers
// never wait for disk I/O; every cooperating writer and rotator uses one lock.
package runtimelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/redact"
)

var processID = events.NewRunID()

const queueSize = 1024
const maxRecordBytes = 8192

// Logger owns a bounded queue and a background writer. Close is bounded even
// when a filesystem stops responding. Records are best effort, not an audit WAL.
type Logger struct {
	path      string
	maxBytes  int64
	surface   string
	mu        sync.Mutex
	closed    bool
	queue     chan []byte
	done      chan struct{}
	dropped   atomic.Uint64
	failures  atomic.Uint64
	abort     chan struct{}
	abortOnce sync.Once
	write     func([]byte) error
}

func Open(path, surface string, maxMB int64) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Validate before accepting work, even if no event is ever emitted.
	f, err := openPrivate(path)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	l := &Logger{path: path, surface: surface, queue: make(chan []byte, queueSize), done: make(chan struct{}), abort: make(chan struct{})}
	if maxMB > 0 {
		l.maxBytes = math.MaxInt64
		if maxMB <= math.MaxInt64/(1<<20) {
			l.maxBytes = maxMB << 20
		}
	}
	l.write = l.append
	go l.run()
	return l, nil
}

func openPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = fmt.Errorf("runtime log must be a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// lock bounds lock contention and rejects symlinks using the same hardened
// opener as the log. flock then locks that stable, private sidecar inode.
func lock(path string) (func(), error) {
	f, err := openPrivate(path + ".lock")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		release, err := flock.TryLockFile(f)
		if err == nil {
			return func() { release(); _ = f.Close() }, nil
		}
		if !errors.Is(err, flock.ErrLocked) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (l *Logger) append(b []byte) error {
	release, err := lock(l.path)
	if err != nil {
		return err
	}
	defer release()
	if l.maxBytes > 0 {
		if _, err := rotateLocked(l.path, l.maxBytes); err != nil {
			return err
		}
	}
	f, err := openPrivate(l.path)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Rotate participates in the writers' sidecar-lock protocol. It is also used
// by the maintenance janitor; the caller supplies a byte limit.
func Rotate(path string, maxBytes int64) (bool, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	}
	release, err := lock(path)
	if err != nil {
		return false, err
	}
	defer release()
	return rotateLocked(path, maxBytes)
}
func rotateLocked(path string, maxBytes int64) (bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() {
		return false, fmt.Errorf("runtime log must be a regular file")
	}
	if st.Size() <= maxBytes {
		return false, nil
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return false, err
	}
	f, err := openPrivate(path)
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

// Emit applies a field allowlist regardless of other event sinks' argument
// settings. Neither redaction nor truncation is a substitute for this boundary.
func (l *Logger) Emit(ev events.Event) {
	ev, ok := Sanitize(ev)
	if !ok {
		return
	}
	level := "INFO"
	switch ev.Type {
	case "run_failed", "subagent_failed", "llm_call_failed", "operation_failed", "panic_recovered":
		level = "ERROR"
	case "operation_warning", "tool_call_failed", "subagent_denied", "budget_exceeded", "budget_warning", "tool_recovery", "logging_dropped":
		level = "WARN"
	}
	if ev.Type == "subagent_completed" {
		status, _ := ev.Data["status"].(string)
		switch status {
		case "success", "completed":
		case "partial", "cancelled":
			level = "WARN"
		default:
			level = "ERROR"
		}
	}
	record := struct {
		events.Event
		Level     string `json:"level"`
		Surface   string `json:"surface,omitempty"`
		ProcessID string `json:"process_id"`
		PID       int    `json:"pid"`
	}{ev, level, l.surface, processID, os.Getpid()}
	b, err := json.Marshal(record)
	if err != nil || len(b) > maxRecordBytes {
		l.dropped.Add(1)
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	select {
	case l.queue <- b:
	default:
		l.dropped.Add(1)
	}
}

func (l *Logger) run() {
	defer close(l.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var reportedDrops, reportedFailures uint64
	report := func() {
		drops, failures := l.dropped.Load(), l.failures.Load()
		if drops != reportedDrops || failures != reportedFailures {
			// Fixed diagnostics: never print filesystem errors or producer content.
			fmt.Fprintf(os.Stderr, "odek: runtime logging lost records: dropped=%d write_failures=%d\n", drops, failures)
			reportedDrops, reportedFailures = drops, failures
		}
	}
	defer report()
	for {
		select {
		case b, ok := <-l.queue:
			if !ok {
				return
			}
			batch := append([]byte(nil), b...)
			for i := 0; i < 63; i++ {
				select {
				case next, ok := <-l.queue:
					if ok {
						batch = append(batch, next...)
					}
				default:
					i = 63
				}
			}
			for {
				err := l.write(batch)
				if errors.Is(err, flock.ErrLocked) {
					select {
					case <-l.abort:
						l.failures.Add(1)
						return
					case <-time.After(10 * time.Millisecond):
						continue
					}
				}
				if err != nil {
					l.failures.Add(1)
				}
				break
			}
		case <-ticker.C:
			report()
		}
	}
}

func (l *Logger) Dropped() uint64 { return l.dropped.Load() }
func (l *Logger) Close() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.queue)
	}
	l.mu.Unlock()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		l.abortOnce.Do(func() { close(l.abort) })
		fmt.Fprintln(os.Stderr, "odek: runtime logging shutdown timed out; records may be lost")
	}
}

var allowedTypes = map[string]bool{}

func init() {
	for _, s := range []string{"operation_failed", "operation_warning", "panic_recovered", "run_started", "turn_started", "run_completed", "run_failed", "iteration_completed", "tool_call_started", "tool_call_executing", "tool_execution_completed", "tool_call_completed", "tool_call_failed", "llm_call_started", "llm_call_completed", "llm_call_failed", "session_saved", "context_trimmed", "budget_exceeded", "budget_warning", "tool_recovery", "subagent_slot_acquired", "subagent_queued", "subagent_spawned", "subagent_completed", "subagent_failed", "subagent_running", "subagent_denied", "subagent_concurrency_wait", "subagent_started", "side_call_usage", "logging_dropped"} {
		allowedTypes[s] = true
	}
}

// Sanitize returns a fresh metadata-only event, also suitable for child IPC.
// Unknown event types and non-scalar values are rejected, never serialized.
func Sanitize(ev events.Event) (events.Event, bool) {
	if !allowedTypes[ev.Type] {
		return events.Event{}, false
	}
	ev.Schema = events.Schema
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	ev.Tool = short(ev.Tool)
	ev.RunID = short(ev.RunID)
	ev.SessionID = short(ev.SessionID)
	ev.TurnID = short(ev.TurnID)
	ev.RootRunID = short(ev.RootRunID)
	ev.ParentRunID = short(ev.ParentRunID)
	ev.TaskID = short(ev.TaskID)
	ev.ParentTaskID = short(ev.ParentTaskID)
	ev.ParentTurnID = short(ev.ParentTurnID)
	ev.SourceTaskID = short(ev.SourceTaskID)
	data := make(map[string]any)
	for k, v := range ev.Data {
		switch k {
		case "component", "operation", "error_type", "call_id", "model", "profile", "max_risk", "status", "exit_status", "error_class", "class", "activity", "limit_name", "kind", "mode", "task_id":
			if s, ok := v.(string); ok {
				data[k] = short(s)
			}
		case "http_status", "count", "threshold_percent", "pid", "depth", "timeout_seconds", "max_iterations", "iterations", "duration_ms", "duration_seconds", "elapsed_seconds", "last_event_age_seconds", "active_calls", "pending_calls", "tokens_used", "input_tokens", "output_tokens", "cost_usd", "artifact_count", "result_bytes", "task_index", "waited_ms", "tools_called", "call_duration_ms", "ttft_ms", "generation_ms", "call_input_tokens", "call_output_tokens", "cache_read", "cache_create", "budget_seconds", "budget_iterations", "budget_cost_usd", "exit_code", "stderr_bytes", "message_count", "dropped_groups", "truncated_results", "observed", "limit", "dropped", "queue_wait_ms":
			switch v.(type) {
			case int, int32, int64, uint, uint64, float64, float32:
				data[k] = v
			}
		case "sandbox":
			if b, ok := v.(bool); ok {
				data[k] = b
			}
		}
	}
	ev.Data = data
	return ev, true
}
func short(s string) string {
	s = redact.RedactSecrets(s)
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}
