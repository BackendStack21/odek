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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/flock"
	"github.com/BackendStack21/odek/internal/redact"
)

var processID = events.NewRunID()
var recordSequence atomic.Uint64

const queueSize = 1024
const priorityQueueSize = 128
const maxRecordBytes = 8192

// ErrorInfo contains only operator-safe, structured failure metadata.
type ErrorInfo struct {
	Code       string `json:"code,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
}

// Record is the stable on-disk odek.log/v1 envelope. Metadata is allowlisted
// and scalar-only; callers must not place prompts, arguments, results, or paths
// in it.
type Record struct {
	Schema       string         `json:"schema"`
	Timestamp    time.Time      `json:"timestamp"`
	Level        string         `json:"level"`
	Event        string         `json:"event"`
	Surface      string         `json:"surface,omitempty"`
	ProcessID    string         `json:"process_id"`
	RecordID     string         `json:"record_id"`
	PID          int            `json:"pid"`
	SessionID    string         `json:"session_id,omitempty"`
	TurnID       string         `json:"turn_id,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	ParentRunID  string         `json:"parent_run_id,omitempty"`
	RootRunID    string         `json:"root_run_id,omitempty"`
	TaskID       string         `json:"task_id,omitempty"`
	SourceTaskID string         `json:"source_task_id,omitempty"`
	ParentTaskID string         `json:"parent_task_id,omitempty"`
	ParentTurnID string         `json:"parent_turn_id,omitempty"`
	CallID       string         `json:"call_id,omitempty"`
	Status       string         `json:"status,omitempty"`
	DurationMS   int64          `json:"duration_ms,omitempty"`
	Error        *ErrorInfo     `json:"error,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Options configures a process logger. MaxFiles counts the active file.
type Options struct {
	Path        string
	Surface     string
	Level       string
	MaxFileMB   int64
	MaxFiles    int
	MaxAgeHours int
}

const Schema = "odek.log/v1"

// DefaultOptions returns the default enabled logger policy. The caller may
// replace Path after expanding the operator's home directory.
func DefaultOptions() Options {
	return Options{Path: "~/.odek/runtime.log", Level: "info", MaxFileMB: 25, MaxFiles: 4, MaxAgeHours: 168}
}

// Logger owns a bounded queue and a background writer. Close is bounded even
// when a filesystem stops responding. Records are best effort, not an audit WAL.
type Logger struct {
	path      string
	maxBytes  int64
	maxFiles  int
	surface   string
	level     string
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
	return OpenWithOptions(Options{Path: path, Surface: surface, Level: "info", MaxFileMB: maxMB, MaxFiles: 2})
}

// OpenWithOptions creates a bounded writer using a stable lock shared with
// retention and maintenance. The default retention policy is four total files.
func OpenWithOptions(opts Options) (*Logger, error) {
	path, surface := opts.Path, opts.Surface
	if path == "" {
		return nil, fmt.Errorf("runtime log path is required")
	}
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	opts.Path = path
	if opts.Level != "" {
		opts.Level = strings.ToLower(opts.Level)
		if opts.Level != "debug" && opts.Level != "info" && opts.Level != "warn" && opts.Level != "error" {
			opts.Level = "info"
		}
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 4
	}
	if opts.MaxFiles > 32 {
		opts.MaxFiles = 32
	}
	if opts.Level == "" {
		opts.Level = "info"
	}
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
	l := &Logger{path: path, surface: surface, queue: make(chan []byte, queueSize), done: make(chan struct{}), abort: make(chan struct{}), maxFiles: opts.MaxFiles, level: strings.ToLower(opts.Level)}
	if opts.MaxFileMB > 0 {
		l.maxBytes = math.MaxInt64
		if opts.MaxFileMB <= math.MaxInt64/(1<<20) {
			l.maxBytes = opts.MaxFileMB << 20
		}
	}
	l.write = l.append
	go l.run()
	return l, nil
}

type sharedLogger struct {
	logger *Logger
	refs   int
	opts   Options
}

var sharedMu sync.Mutex
var sharedLoggers = map[string]*sharedLogger{}

// Acquire returns the one writer for a configured path within this process.
// Each owner must call release exactly once; the final release drains/closes it.
func Acquire(opts Options) (*Logger, func(), error) {
	path, err := expandHome(opts.Path)
	if err != nil {
		return nil, nil, err
	}
	opts.Path = path
	key, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, nil, err
	}
	opts.Path = key
	sharedMu.Lock()
	if existing := sharedLoggers[key]; existing != nil {
		want, have := opts, existing.opts
		want.Surface, have.Surface = "", ""
		if have != want {
			sharedMu.Unlock()
			return nil, nil, fmt.Errorf("runtime log already acquired with different options")
		}
		existing.refs++
		sharedMu.Unlock()
		return existing.logger, releaseFor(key, existing.logger), nil
	}
	l, err := OpenWithOptions(opts)
	if err != nil {
		sharedMu.Unlock()
		return nil, nil, err
	}
	entry := &sharedLogger{logger: l, refs: 1, opts: opts}
	sharedLoggers[key] = entry
	sharedMu.Unlock()
	return l, releaseFor(key, l), nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

func releaseFor(key string, l *Logger) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			sharedMu.Lock()
			entry := sharedLoggers[key]
			last := false
			if entry != nil && entry.logger == l {
				entry.refs--
				if entry.refs <= 0 {
					delete(sharedLoggers, key)
					last = true
				}
			}
			sharedMu.Unlock()
			if last {
				l.Close()
			}
		})
	}
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
		if _, err := rotateNLocked(l.path, l.maxBytes, l.maxFiles); err != nil {
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
	return RotateWithOptions(path, maxBytes, 2)
}

// RotateWithOptions rotates a log under its stable lock. maxFiles includes
// the active file; generations are named .1 through .(maxFiles-1).
func RotateWithOptions(path string, maxBytes int64, maxFiles int) (bool, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	}
	release, err := lock(path)
	if err != nil {
		return false, err
	}
	defer release()
	return rotateNLocked(path, maxBytes, maxFiles)
}
func rotateLocked(path string, maxBytes int64) (bool, error) {
	return rotateNLocked(path, maxBytes, 2)
}
func rotateNLocked(path string, maxBytes int64, maxFiles int) (bool, error) {
	if maxFiles < 1 {
		maxFiles = 1
	}
	if maxFiles > 32 {
		maxFiles = 32
	}
	if err := removeExcessBackupsLocked(path, maxFiles); err != nil {
		return false, err
	}
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
	if maxFiles <= 1 {
		if err := os.Remove(path); err != nil {
			return false, err
		}
	} else {
		oldest := path + "." + strconv.Itoa(maxFiles-1)
		if err := removeRegular(oldest); err != nil {
			return false, err
		}
		for i := maxFiles - 2; i >= 1; i-- {
			src := path + "." + strconv.Itoa(i)
			dst := path + "." + strconv.Itoa(i+1)
			if _, err := os.Lstat(src); err == nil {
				if err := os.Rename(src, dst); err != nil {
					return false, err
				}
			} else if !os.IsNotExist(err) {
				return false, err
			}
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return false, err
		}
	}
	f, err := openPrivate(path)
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

func removeExcessBackupsLocked(path string, maxFiles int) error {
	paths, err := ExcessBackupPaths(path, maxFiles)
	if err != nil {
		return err
	}
	for _, full := range paths {
		if err := removeRegular(full); err != nil {
			return err
		}
	}
	return nil
}

// ExcessBackupPaths lists numbered backups beyond maxFiles (which counts the
// active file). It is read-only and intended for cleanup previews.
func ExcessBackupPaths(path string, maxFiles int) ([]string, error) {
	if maxFiles < 1 {
		maxFiles = 1
	}
	if maxFiles > 32 {
		maxFiles = 32
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	var paths []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		suffix := strings.TrimPrefix(name, base+".")
		n, err := strconv.Atoi(suffix)
		if err != nil || n < maxFiles {
			continue
		}
		full := filepath.Join(filepath.Dir(path), name)
		info, err := os.Lstat(full)
		if err != nil {
			return paths, err
		}
		if !info.Mode().IsRegular() {
			return paths, fmt.Errorf("runtime log backup must be a regular file")
		}
		paths = append(paths, full)
	}
	return paths, nil
}

func removeRegular(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("runtime log backup must be a regular file")
	}
	return os.Remove(path)
}

// Emit applies a field allowlist regardless of other event sinks' argument
// settings. Neither redaction nor truncation is a substitute for this boundary.
func (l *Logger) Emit(ev events.Event) {
	l.EmitForSurface(ev, l.surface)
}

// EmitForSurface keeps one process/path writer while recording the producer's
// own surface label. It is useful when a process hosts several surfaces.
func (l *Logger) EmitForSurface(ev events.Event, surface string) {
	ev, ok := Sanitize(ev)
	if !ok {
		return
	}
	r := legacyRecord(ev, surface)
	l.EmitRecord(r)
}

// EmitRecord sanitizes and queues a v1 record. Warnings and errors use a
// reserved queue lane so a burst of debug/info events cannot starve them.
func (l *Logger) EmitRecord(rec Record) {
	rec = sanitizeRecord(rec)
	rec.RecordID = processID + "-" + strconv.FormatUint(recordSequence.Add(1), 10)
	if rec.Event == "" || !levelAtLeast(rec.Level, l.level) {
		return
	}
	b, err := json.Marshal(rec)
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
	priority := rec.Level == "warn" || rec.Level == "error" || rec.Event == "run.started" || rec.Event == "run.finished" || rec.Event == "service.started" || rec.Event == "service.stopped"
	if !priority && len(l.queue) >= cap(l.queue)-priorityQueueSize {
		l.dropped.Add(1)
		return
	}
	select {
	case l.queue <- b:
	default:
		l.dropped.Add(1)
	}
}

func legacyRecord(ev events.Event, surface string) Record {
	level := "info"
	switch ev.Type {
	case "run_failed", "subagent_failed", "llm_call_failed", "operation_failed", "panic_recovered":
		level = "error"
	case "operation_warning", "tool_call_failed", "subagent_denied", "budget_exceeded", "budget_warning", "tool_recovery", "logging_dropped", "request_rejected":
		level = "warn"
	}
	switch ev.Type {
	case "tool_call_started", "tool_call_executing", "tool_execution_completed", "tool_call_completed", "llm_call_started", "llm_call_completed", "iteration_completed", "turn_started", "subagent_running", "subagent_queued", "subagent_slot_acquired", "subagent_concurrency_wait", "side_call_usage", "operation_debug":
		level = "debug"
	case "operation_info", "service_started", "service_stopped", "run_finished", "schedule_delivered":
		level = "info"
	}
	var status string
	if ev.Type == "subagent_completed" {
		status, _ = ev.Data["status"].(string)
		switch status {
		case "success", "completed":
		case "partial":
			level = "warn"
		case "cancelled":
			level = "info"
		default:
			level = "error"
		}
	}
	eventName := map[string]string{"run_started": "run.started", "run_completed": "run.finished", "run_failed": "run.finished", "run_finished": "run.finished", "service_started": "service.started", "service_stopped": "service.stopped", "request_rejected": "request.rejected", "operation_info": "operation.info", "operation_debug": "operation.debug", "schedule_delivered": "schedule.delivered", "subagent_started": "subagent.started", "subagent_completed": "subagent.finished", "subagent_failed": "subagent.finished", "tool_call_started": "tool.started", "tool_call_completed": "tool.finished", "tool_call_failed": "tool.failed", "llm_call_started": "model.call_started", "llm_call_completed": "model.call_finished", "llm_call_failed": "model.call_failed", "session_saved": "session.saved", "iteration_completed": "iteration.completed", "context_trimmed": "context.trimmed"}[ev.Type]
	if eventName == "" {
		eventName = strings.ReplaceAll(ev.Type, "_", ".")
	}
	r := Record{Schema: Schema, Timestamp: ev.Timestamp, Level: level, Event: eventName, Surface: surface, ProcessID: processID, PID: os.Getpid(), SessionID: ev.SessionID, TurnID: ev.TurnID, RunID: ev.RunID, ParentRunID: ev.ParentRunID, RootRunID: ev.RootRunID, TaskID: ev.TaskID, SourceTaskID: ev.SourceTaskID, ParentTaskID: ev.ParentTaskID, ParentTurnID: ev.ParentTurnID, Status: status}
	if ev.Data != nil {
		if status == "" {
			r.Status, _ = ev.Data["status"].(string)
		}
		switch v := ev.Data["duration_ms"].(type) {
		case int:
			r.DurationMS = int64(v)
		case int64:
			r.DurationMS = v
		case float64:
			r.DurationMS = int64(v)
		}
		if r.Status == "" && ev.Type == "run_completed" {
			r.Status = "completed"
		}
		if r.Status == "" && ev.Type == "run_failed" {
			r.Status = "failed"
		}
		if class, _ := ev.Data["error_class"].(string); class == "context_canceled" && (ev.Type == "run_failed" || ev.Type == "subagent_failed") {
			r.Status = "cancelled"
			r.Level = "info"
		}
		if v, ok := ev.Data["call_id"].(string); ok {
			r.CallID = v
		}
		r.Metadata = make(map[string]any)
		for k, v := range ev.Data {
			r.Metadata[k] = v
		}
	}
	if r.Status != "cancelled" && (ev.Type == "tool_call_failed" || ev.Type == "run_failed" || ev.Type == "llm_call_failed" || ev.Type == "subagent_failed" || ev.Type == "operation_failed") {
		code, _ := ev.Data["error_class"].(string)
		httpStatus, _ := ev.Data["http_status"].(int)
		attempts, _ := ev.Data["attempts"].(int)
		if httpStatus == 0 {
			httpStatus = scalarInt(ev.Data["http_status"])
		}
		if attempts == 0 {
			attempts = scalarInt(ev.Data["attempts"])
		}
		r.Error = &ErrorInfo{Code: canonicalErrorCode(code), HTTPStatus: httpStatus, Attempts: attempts}
	}
	return r
}

func scalarInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float32:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func canonicalErrorCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "provider_auth":
		return "provider.auth"
	case "rate_limited":
		return "provider.rate_limited"
	case "context_canceled":
		return "context.cancelled"
	case "deadline_exceeded":
		return "deadline.exceeded"
	case "execution_budget":
		return "execution.budget"
	case "invalid_file_type":
		return "file.invalid_type"
	case "symlink_rejected":
		return "file.symlink_rejected"
	}
	return strings.ReplaceAll(s, "_", ".")
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
	queue := (<-chan []byte)(l.queue)
	for queue != nil {
		select {
		case b, ok := <-queue:
			if !ok {
				queue = nil
				continue
			}
			batch, n := drainBatch(append([]byte(nil), b...), queue)
			if err := l.flushBatch(batch); err != nil {
				l.failures.Add(uint64(n))
			}
		case <-ticker.C:
			report()
		}
	}
}

// maxBatchBytes bounds how much already-queued output one write combines.
const maxBatchBytes = 64 << 10

// drainBatch appends records that are already queued to first, without
// blocking, until maxBatchBytes is reached. Order is preserved. It returns the
// combined bytes and the number of records in them.
func drainBatch(first []byte, queue <-chan []byte) ([]byte, int) {
	n := 1
	for len(first) < maxBatchBytes {
		select {
		case b, ok := <-queue:
			if !ok {
				return first, n
			}
			first = append(first, b...)
			n++
		default:
			return first, n
		}
	}
	return first, n
}

func (l *Logger) flushBatch(batch []byte) error {
	for {
		err := l.write(batch)
		if errors.Is(err, flock.ErrLocked) {
			select {
			case <-l.abort:
				return err
			case <-time.After(10 * time.Millisecond):
				continue
			}
		}
		return err
	}
}

func (l *Logger) Dropped() uint64 { return l.dropped.Load() }
func (l *Logger) Close() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		if l.queue != nil {
			close(l.queue)
		}
	}
	l.mu.Unlock()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		l.abortOnce.Do(func() { close(l.abort) })
		fmt.Fprintln(os.Stderr, "odek: runtime logging shutdown timed out; records may be lost")
	}
}

func sanitizeRecord(r Record) Record {
	r.Schema = Schema
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	r.Level = strings.ToLower(r.Level)
	if r.Level != "debug" && r.Level != "info" && r.Level != "warn" && r.Level != "error" {
		r.Level = "info"
	}
	r.Event = short(r.Event)
	r.Surface = short(r.Surface)
	if r.ProcessID == "" {
		r.ProcessID = processID
	}
	r.ProcessID, r.SessionID, r.TurnID = short(r.ProcessID), short(r.SessionID), short(r.TurnID)
	r.RunID, r.ParentRunID, r.RootRunID = short(r.RunID), short(r.ParentRunID), short(r.RootRunID)
	r.TaskID, r.SourceTaskID, r.ParentTaskID = short(r.TaskID), short(r.SourceTaskID), short(r.ParentTaskID)
	r.ParentTurnID, r.CallID, r.Status = short(r.ParentTurnID), short(r.CallID), short(r.Status)
	if r.PID == 0 {
		r.PID = os.Getpid()
	}
	metadata := make(map[string]any)
	for k, v := range r.Metadata {
		switch k {
		case "component", "operation", "error_type", "model", "profile", "max_risk", "status", "exit_status", "error_class", "class", "activity", "limit_name", "kind", "mode", "task_id", "http_status", "attempts", "count", "threshold_percent", "pid", "depth", "timeout_seconds", "max_iterations", "iterations", "duration_ms", "duration_seconds", "elapsed_seconds", "last_event_age_seconds", "active_calls", "pending_calls", "tokens_used", "input_tokens", "output_tokens", "cost_usd", "artifact_count", "result_bytes", "task_index", "waited_ms", "tools_called", "call_duration_ms", "ttft_ms", "generation_ms", "call_input_tokens", "call_output_tokens", "cache_read", "cache_create", "budget_seconds", "budget_iterations", "budget_cost_usd", "exit_code", "stderr_bytes", "message_count", "dropped_groups", "truncated_results", "observed", "limit", "dropped", "queue_wait_ms", "sandbox":
			switch x := v.(type) {
			case string:
				if safeMetadataString(x) {
					metadata[k] = short(x)
				}
			case bool:
				metadata[k] = x
			case int, int32, int64, uint, uint64, float32, float64:
				metadata[k] = x
			}
		}
	}
	r.Metadata = metadata
	if r.Error != nil {
		e := *r.Error
		e.Code = short(e.Code)
		r.Error = &e
	}
	return r
}

func safeMetadataString(s string) bool {
	if len(s) > 96 || strings.Contains(s, "..") {
		return false
	}
	if strings.ContainsAny(s, "/{}") {
		// Permit static route templates used by the HTTP diagnostics adapter;
		// ordinary filesystem paths and URLs are not metadata.
		if !strings.ContainsAny(s, "{}") {
			return false
		}
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.:-/{}", r) {
			continue
		}
		return false
	}
	return true
}

func levelAtLeast(got, min string) bool {
	order := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
	return order[got] >= order[min]
}

var allowedTypes = map[string]bool{}

func init() {
	for _, s := range []string{"operation_failed", "operation_warning", "operation_info", "operation_debug", "panic_recovered", "request_rejected", "service_started", "service_stopped", "run_finished", "run_started", "turn_started", "run_completed", "run_failed", "iteration_completed", "tool_call_started", "tool_call_executing", "tool_execution_completed", "tool_call_completed", "tool_call_failed", "llm_call_started", "llm_call_completed", "llm_call_failed", "session_saved", "context_trimmed", "budget_exceeded", "budget_warning", "tool_recovery", "subagent_slot_acquired", "subagent_queued", "subagent_spawned", "subagent_completed", "subagent_failed", "subagent_running", "subagent_denied", "subagent_concurrency_wait", "subagent_started", "side_call_usage", "logging_dropped", "schedule_delivered"} {
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
		case "http_status", "attempts", "count", "threshold_percent", "pid", "depth", "timeout_seconds", "max_iterations", "iterations", "duration_ms", "duration_seconds", "elapsed_seconds", "last_event_age_seconds", "active_calls", "pending_calls", "tokens_used", "input_tokens", "output_tokens", "cost_usd", "artifact_count", "result_bytes", "task_index", "waited_ms", "tools_called", "call_duration_ms", "ttft_ms", "generation_ms", "call_input_tokens", "call_output_tokens", "cache_read", "cache_create", "budget_seconds", "budget_iterations", "budget_cost_usd", "exit_code", "stderr_bytes", "message_count", "dropped_groups", "truncated_results", "observed", "limit", "dropped", "queue_wait_ms":
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
