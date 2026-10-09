// Package events implements odek's structured runtime event stream
// (schema odek.event/v1, see docs/EXTENSIONS.md): a small Event type, a
// non-blocking panic-isolated Emitter that fans events out to a handler, and
// an append-only JSONL sink (jsonl.go).
//
// Events are observability data for orchestrators. They never carry raw tool
// arguments (only a SHA-256 digest and sizes), never carry environment
// variables or credentials, and human-readable string fields pass through
// internal/redact before dispatch.
package events

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/redact"
)

// Schema is the event envelope schema identifier. All schemas are additive:
// consumers must ignore unknown fields and unknown event types.
const Schema = "odek.event/v1"

// Event types emitted by odek. Consumers must ignore unknown types.
const (
	TypeRunStarted            = "run_started"
	TypeIterationCompleted    = "iteration_completed"
	TypeToolCallStarted       = "tool_call_started"
	TypeToolCallCompleted     = "tool_call_completed"
	TypeToolCallFailed        = "tool_call_failed"
	TypeSessionSaved          = "session_saved"
	TypeContextTrimmed        = "context_trimmed"
	TypeBudgetExceeded        = "budget_exceeded"
	TypeRunCompleted          = "run_completed"
	TypeRunFailed             = "run_failed"
	TypePlanCreated           = "plan_created"
	TypePlanUpdated           = "plan_updated"
	TypePlanBlocked           = "plan_blocked"
	TypePlanReassessment      = "plan_reassessment"
	TypePlanValidationFailed  = "plan_validation_failed"
	TypeSubagentSpawned       = "subagent_spawned"
	TypeSubagentCompleted     = "subagent_completed"
	TypeSideCallUsage         = "side_call_usage"
	TypeVerificationStarted   = "verification_started"
	TypeVerificationCompleted = "verification_completed"
)

// Budget limit names carried in budget_exceeded events (data.limit_name).
// The constants are defined now so producers and consumers share one
// vocabulary; the enforcement that triggers these events lands with the
// execution-budget work.
const (
	LimitRuntime      = "runtime"
	LimitToolCalls    = "tool_calls"
	LimitInputTokens  = "input_tokens"
	LimitOutputTokens = "output_tokens"
	LimitCostUSD      = "cost_usd"
)

// Context identifies an invocation and its delegation ancestry. SessionID is
// inherited by children; TurnID distinguishes calls on a reused Agent.
type Context struct {
	RunID        string `json:"run_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	RootRunID    string `json:"root_run_id,omitempty"`
	ParentRunID  string `json:"parent_run_id,omitempty"`
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
}

// Event is a single structured runtime event (schema odek.event/v1).
//
// Not every field is set for every Type; the zero value means "not
// applicable" and is omitted from the JSON form. Data carries the per-type
// fields documented in docs/EXTENSIONS.md.
type Event struct {
	SourceTaskID string         `json:"source_task_id,omitempty"`
	TurnID       string         `json:"turn_id,omitempty"`
	RootRunID    string         `json:"root_run_id,omitempty"`
	ParentRunID  string         `json:"parent_run_id,omitempty"`
	ParentTurnID string         `json:"parent_turn_id,omitempty"`
	TaskID       string         `json:"task_id,omitempty"`
	ParentTaskID string         `json:"parent_task_id,omitempty"`
	Schema       string         `json:"schema"`
	Type         string         `json:"type"`
	RunID        string         `json:"run_id,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	Iteration    int            `json:"iteration,omitempty"`
	Tool         string         `json:"tool,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	Data         map[string]any `json:"data,omitempty"`
}

// NewRunID returns a random 128-bit hex run identifier. A fresh ID is
// generated per agent run and carried through every event of that run.
func NewRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is not a reason to die; fall back to a
		// time-derived ID (collisions only weaken correlation, not safety).
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

// ArgsDigest returns the SHA-256 hex digest of raw tool-call arguments.
// Events carry this digest plus the argument byte size — never the raw
// arguments — so a start/complete pair can be correlated without leaking
// potentially secret argument content into the event stream.
func ArgsDigest(args string) string {
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:])
}

// ErrorClass maps an error to a stable, low-cardinality class string for
// tool_call_failed / run_failed events. Raw error text is never emitted:
// it may contain attacker-controlled or secret-bearing content.
func ErrorClass(err error) string {
	return classifyError(err)
}

// DefaultBufferSize is the default capacity of an Emitter's dispatch queue.
// When the queue is full, new events are dropped (and counted) rather than
// blocking the agent loop.
const DefaultBufferSize = 1024

// Emitter fans events out to a handler from a dedicated goroutine. Emit is
// non-blocking (drop-on-full) and the handler is panic-isolated: a slow or
// panicking handler can never stall or crash the agent loop.
//
// The Emitter stamps Schema, Timestamp, RunID, and SessionID centrally, and
// redacts secret-looking content from human-readable fields (Tool and string
// values in Data) before dispatch.
type Emitter struct {
	handler func(Event)
	ch      chan Event
	wg      sync.WaitGroup

	mu        sync.RWMutex // guards runID, sessionID, and closed
	runID     string
	sessionID string
	context   Context
	closed    bool

	// dispatchGoroutine identifies the dispatch goroutine so Close can tell
	// a reentrant call (made from inside a handler, which runs ON that
	// goroutine) from an external call that merely races an active delivery.
	// An external caller must always block until drained; only the dispatch
	// goroutine itself would deadlock on its own WaitGroup.
	dispatchGoroutine atomic.Uint64
	inDispatch        atomic.Bool
	closeOnce         sync.Once

	dropped atomic.Uint64
}

// NewEmitter creates an Emitter that dispatches to handler. runID is stamped
// on every event; pass NewRunID() for a fresh run. A nil handler discards
// events (still useful for tests that only count drops).
func NewEmitter(handler func(Event), runID string) *Emitter {
	e := &Emitter{
		handler: handler,
		ch:      make(chan Event, DefaultBufferSize),
		runID:   runID,
	}
	e.wg.Add(1)
	go e.dispatch()
	return e
}

// dispatch drains the queue in FIFO order until the channel is closed.
// A panicking handler is recovered so one bad event cannot kill the stream.
func (e *Emitter) dispatch() {
	e.dispatchGoroutine.Store(currentGoroutineID())
	defer e.wg.Done()
	for ev := range e.ch {
		e.deliver(ev)
	}
}

// currentGoroutineID parses the running goroutine's ID from its stack
// header ("goroutine N [running]:"). This is the only reliable way to
// distinguish "Close called from the handler" (same goroutine as dispatch)
// from "Close called concurrently while a delivery is in flight" — the two
// need opposite waiting behavior. Runtime-stack parsing is a stable,
// widely-used trick; failure parses to 0, which never matches a real ID.
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (e *Emitter) deliver(ev Event) {
	if e.handler == nil {
		return
	}
	e.inDispatch.Store(true)
	defer e.inDispatch.Store(false)
	defer func() { _ = recover() }()
	e.handler(ev)
}

// Emit stamps and queues an event for dispatch. It never blocks: when the
// queue is full the event is dropped and counted (see Dropped). Emit after
// Close is a no-op.
func (e *Emitter) Emit(ev Event) {
	if ev.Schema == "" {
		ev.Schema = Schema
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	// Redaction only touches the event's own Tool/Data, so it runs before the
	// lock: concurrent emitters do not serialize on regex scanning.
	redactEvent(&ev)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if ev.RunID == "" || ev.RunID == e.runID {
		ev.RunID = e.runID
		if ev.TurnID == "" {
			ev.TurnID = e.context.TurnID
		}
		if ev.TaskID == "" {
			ev.TaskID = e.context.TaskID
		}
		if ev.ParentTaskID == "" {
			ev.ParentTaskID = e.context.ParentTaskID
		}
		if ev.ParentRunID == "" {
			ev.ParentRunID = e.context.ParentRunID
		}
		if ev.ParentTurnID == "" {
			ev.ParentTurnID = e.context.ParentTurnID
		}
	}
	if ev.RootRunID == "" {
		ev.RootRunID = e.context.RootRunID
		if ev.RootRunID == "" {
			ev.RootRunID = e.runID
		}
	}
	if ev.SessionID == "" {
		ev.SessionID = e.sessionID
	}

	select {
	case e.ch <- ev:
	default:
		e.dropped.Add(1)
	}
}

// redactEvent scrubs secret-looking content from the human-readable fields
// of an event before it leaves the process. The caller's Data map is never
// written through: Event is passed by value but maps are reference types,
// and mutating shared state (or racing on it across concurrent Emits) is
// not acceptable — so string values are redacted into a fresh map.
func redactEvent(ev *Event) {
	if ev.Tool != "" {
		ev.Tool = redact.RedactSecrets(ev.Tool)
	}
	if len(ev.Data) == 0 {
		return
	}
	data := make(map[string]any, len(ev.Data))
	for k, v := range ev.Data {
		data[k] = redactValue(v)
	}
	ev.Data = data
}

// redactValue redacts strings at any nesting depth of map/slice values.
// Event Data is built at call sites that may embed structured values
// (sub-agent results, tool outputs); the event stream is a documented
// redaction surface, so a nested secret must not slip through to
// handlers, the JSONL sink, or WS consumers by reference.
func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return redact.RedactSecrets(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = redactValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = redactValue(vv)
		}
		return out
	default:
		return v
	}
}

// SetSessionID sets the session identifier stamped on subsequent events.
// Call it as soon as the session is known; earlier events simply carry no
// session_id.
func (e *Emitter) SetSessionID(id string) {
	e.mu.Lock()
	e.sessionID = id
	e.mu.Unlock()
}

// RunID returns the run identifier stamped on events.
func (e *Emitter) RunID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.runID
}

// Dropped returns how many events were discarded because the dispatch queue
// was full.
func (e *Emitter) Dropped() uint64 { return e.dropped.Load() }

// Close stops accepting events, drains the queue, and waits for the dispatch
// goroutine to finish. Safe to call more than once, and safe to call from
// inside a handler: a reentrant call tears down state and returns without
// waiting for itself.
func (e *Emitter) Close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		close(e.ch)
		e.mu.Unlock()
	})
	// A handler runs ON the dispatch goroutine; waiting here for wg would
	// deadlock on our own Done. External callers — including those that
	// race an in-flight delivery from another goroutine — must still wait
	// for the queue to drain.
	if e.inDispatch.Load() && e.dispatchGoroutine.Load() == currentGoroutineID() {
		return
	}
	e.wg.Wait()
}

// SetContext installs trusted correlation metadata before execution begins.
func (e *Emitter) SetContext(c Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.context = c
	if c.SessionID != "" {
		e.sessionID = c.SessionID
	}
}
func (e *Emitter) SetTurnID(id string) { e.mu.Lock(); e.context.TurnID = id; e.mu.Unlock() }

// BeginRun replaces invocation identity atomically. Queued events already carry
// their original context and cannot be relabelled by a subsequent invocation.
func (e *Emitter) BeginRun(runID, turnID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runID = runID
	e.context.RunID = runID
	e.context.TurnID = turnID
	if e.context.ParentRunID == "" {
		e.context.RootRunID = runID
	}
}
func (e *Emitter) Context() Context {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c := e.context
	c.RunID = e.runID
	c.SessionID = e.sessionID
	if c.RootRunID == "" {
		c.RootRunID = e.runID
	}
	return c
}
