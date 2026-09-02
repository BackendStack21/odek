package main

// Wake-on-complete (docs/CONFIG.md `background.wake_on_complete`).
//
// A bg_* job finishing while its session is idle is a dead letter today:
// completion notices drain only inside a running agent loop
// (internal/loop/loop.go, once per iteration), so an idle session never
// learns about the exit until the operator prompts again. The dispatcher
// below turns job exits into system-initiated wake turns for sessions with
// a live WebUI connection:
//
//	job exit (bgproc.Observer, waiter goroutine)
//	  └─ coalesce window (per session; one timer)
//	       └─ dispatch: resolve session → connection(s) via wsConnRegistry
//	            ├─ no connection / busy session → drop (the notice stays in
//	            │  Manager.out and drains at the next turn's iteration 1 —
//	            │  the pre-existing payload path)
//	            └─ idle → guarded enqueue of a synthetic "bg_wake" item into
//	               that connection's prompt queue; the processor loop runs it
//	               like any prompt (same budgets, approvals, usage).
//
// Deliberate limits:
//   - The wake preamble is a generic poke; job details arrive via the
//     normal per-iteration notice drain in the wake turn itself. A preamble
//     listing job ids would DUPLICATE the drained notice (W1).
//   - Busy exclusion is per-SESSION (any bound connection busy ⇒ drop): two
//     clients may bind one session, and a wake turn must never run
//     concurrently with the other connection's turn on the same session
//     store (W3).
//   - Enqueue goes through connWakeSlot: the reader closes the prompt
//     channel on disconnect, and a timer-goroutine send on a closed channel
//     would panic the process. Slot close and channel close happen under
//     the same mutex (W2).
//   - max_wakes_per_hour is a per-session spend control backed by a
//     timestamp window; the config layer clamps it to ≤240/h absolute.

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/redact"
)

// wakeConnState is the dispatcher's view of a session's delivery state.
type wakeConnState int

const (
	wakeNoConn wakeConnState = iota // no connection bound: payload path
	wakeBusy                        // a turn is running on the session: drain covers it
	wakeIdle                        // safe to wake with a system-initiated turn
)

// wakeSettings carries the resolved wake config as primitives.
type wakeSettings struct {
	CoalesceMS   int
	MaxWakesHour int
}

// wakeRouter abstracts session→connection delivery (unit-test seam).
// Implementations must be safe for concurrent use; Post must never block.
type wakeRouter interface {
	State(sessionID string) wakeConnState
	Post(sessionID string, item wsClientMsg) bool
}

// bgWakePreamble is the system-attributed wake turn text. Generic by
// design: the factual completion notice is injected by the loop's
// per-iteration drain at the wake turn's first iteration (redacted and
// audit-ingested there); listing job ids here would duplicate it.
const bgWakePreamble = "[background-jobs] One or more background jobs finished while this session was idle. Their completion notice is attached to this turn — read bg_output for the relevant job id(s) and report the results to the operator."

// wakeDispatcher coalesces job exits per session and starts wake turns.
// It implements bgproc.Observer; methods are called from the manager's
// waiter goroutines and must not block.
type wakeDispatcher struct {
	router     wakeRouter
	coalesce   time.Duration
	maxPerHour int

	mu      sync.Mutex
	pending map[string]int         // session → coalesced exit count
	timers  map[string]*time.Timer // session → coalesce timer
	wakes   map[string][]time.Time // session → wake timestamps (rate limit)
	stopped bool
}

func newWakeDispatcher(router wakeRouter, s wakeSettings) *wakeDispatcher {
	coalesce := time.Duration(s.CoalesceMS) * time.Millisecond
	if coalesce <= 0 {
		coalesce = 2 * time.Second
	}
	maxPerHour := s.MaxWakesHour
	if maxPerHour < 0 {
		maxPerHour = 0
	}
	return &wakeDispatcher{
		router:     router,
		coalesce:   coalesce,
		maxPerHour: maxPerHour,
		pending:    make(map[string]int),
		timers:     make(map[string]*time.Timer),
		wakes:      make(map[string][]time.Time),
	}
}

// BGStarted implements bgproc.Observer (no-op).
func (d *wakeDispatcher) BGStarted(bgproc.Job) {}

// BGExited implements bgproc.Observer: schedule (or join) the session's
// coalesce window.
func (d *wakeDispatcher) BGExited(n bgproc.Notice) {
	if d == nil || n.SessionID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.pending[n.SessionID]++
	if _, ok := d.timers[n.SessionID]; ok {
		return // window already running — coalesce into it
	}
	session := n.SessionID
	d.timers[session] = time.AfterFunc(d.coalesce, func() { d.dispatch(session) })
}

// dispatch fires the coalesced wake for one session (timer goroutine).
func (d *wakeDispatcher) dispatch(session string) {
	d.mu.Lock()
	count := d.pending[session]
	delete(d.pending, session)
	if t, ok := d.timers[session]; ok {
		t.Stop()
		delete(d.timers, session)
	}
	if d.stopped || count == 0 || d.maxPerHour <= 0 {
		d.mu.Unlock()
		return
	}
	// Rate limit: count wakes inside the trailing hour.
	now := time.Now()
	recent := d.wakes[session][:0]
	for _, ts := range d.wakes[session] {
		if now.Sub(ts) < time.Hour {
			recent = append(recent, ts)
		}
	}
	if len(recent) >= d.maxPerHour {
		d.wakes[session] = recent
		d.mu.Unlock()
		return // spend ceiling hit: payload path covers it
	}
	d.mu.Unlock()

	// Router calls happen without d.mu: State/Post take their own locks and
	// Post marshals + enqueues (never blocks, but keep the critical section
	// small regardless).
	if state := d.router.State(session); state != wakeIdle {
		return
	}
	item := wsClientMsg{Type: "bg_wake", SessionID: session}
	if !d.router.Post(session, item) {
		return // full or closed queue: turns are queued anyway — drain covers
	}
	d.mu.Lock()
	d.wakes[session] = append(d.wakes[session], now)
	d.mu.Unlock()
}

// Stop prevents further wakes (serve shutdown). Safe to call repeatedly.
func (d *wakeDispatcher) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for s, t := range d.timers {
		t.Stop()
		delete(d.timers, s)
	}
}

// ── guarded enqueue slot (W2) ────────────────────────────────────────────

// connWakeSlot guards posts into a connection's prompt channel. The reader
// goroutine closes the channel when the socket dies; a dispatcher timer
// goroutine posting concurrently must never touch a closed channel (send
// panics). close() and post() synchronize on mu, and the reader performs
// slot.close() and close(ch) while holding the slot's lock, so a successful
// post always precedes the channel close.
type connWakeSlot struct {
	mu     sync.Mutex
	ch     chan []byte
	closed bool
}

func newConnWakeSlot(ch chan []byte) *connWakeSlot {
	return &connWakeSlot{ch: ch}
}

// post enqueues a raw wire item; reports false when closed or full.
// Never blocks.
func (s *connWakeSlot) post(b []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- b:
		return true
	default:
		return false
	}
}

// close marks the slot closed and closes the channel while still holding
// the lock — the exact teardown pair the reader needs. After close, every
// post observes closed and drops; the lock ordering guarantees no send on
// a closed channel.
func (s *connWakeSlot) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// ── serve wiring ─────────────────────────────────────────────────────────

// bgJobFrame builds the `bg_job` wire frame for a job transition (M2).
// Model/operator-borne text is server-clamped and secret-redacted (W4):
// command_head runs through the same headString clamp as notices plus
// redact.RedactSecrets — a command like `curl -H "Authorization: Bearer
// sk-..."` must not leak its secret into any client. Optional facts are
// represented by absent keys, not zero values, so old clients and future
// statuses degrade cleanly.
func bgJobFrame(n bgproc.Notice, started bool) map[string]any {
	f := map[string]any{
		"type":       "bg_job",
		"job_id":     n.JobID,
		"session_id": n.SessionID,
		"status":     string(n.Status),
		"t":          time.Now().UnixMilli(),
	}
	if n.Command != "" {
		f["command_head"] = redact.RedactSecrets(headString(n.Command, bgCommandHead))
	}
	if !started {
		f["exit_code"] = n.ExitCode
		f["duration_ms"] = n.Duration.Milliseconds()
		f["output_bytes"] = n.OutputBytes
	}
	return f
}

// bgFrameEmitter emits `bg_job` frames on job transitions to every
// connection currently bound to the owning session. Safe to call from
// waiter goroutines: writeWSJSON serializes per connection and fast-fails
// dead ones.
type bgFrameEmitter struct{}

func (bgFrameEmitter) BGStarted(j bgproc.Job) {
	frame := bgJobFrame(bgproc.Notice{JobID: j.ID, SessionID: j.SessionID, Command: j.Command, Status: j.Status}, true)
	for _, c := range wsConnsForSession(j.SessionID) {
		if c.conn != nil {
			writeWSJSON(c.conn, frame)
		}
	}
}

func (bgFrameEmitter) BGExited(n bgproc.Notice) {
	frame := bgJobFrame(n, false)
	for _, c := range wsConnsForSession(n.SessionID) {
		if c.conn != nil {
			writeWSJSON(c.conn, frame)
		}
	}
}

// wsWakeRouter delivers wakes through the live connection registry.
type wsWakeRouter struct{}

func (wsWakeRouter) State(sessionID string) wakeConnState {
	conns := wsConnsForSession(sessionID)
	if len(conns) == 0 {
		return wakeNoConn
	}
	for _, c := range conns {
		if c.isBusy() {
			return wakeBusy // per-SESSION exclusion (W3)
		}
	}
	return wakeIdle
}

// Post delivers the wake item to an idle bound connection's slot.
func (wsWakeRouter) Post(sessionID string, item wsClientMsg) bool {
	raw, err := json.Marshal(item)
	if err != nil {
		return false
	}
	for _, c := range wsConnsForSession(sessionID) {
		if c.isBusy() || c.wakeSlot == nil {
			continue
		}
		if c.wakeSlot.post(raw) {
			// Tell the client the agent is waking for this session so it
			// can render progress without polling /api/jobs.
			if c.conn != nil {
				writeWSJSON(c.conn, map[string]any{
					"type":       "bg_wake",
					"session_id": sessionID,
					"t":          time.Now().UnixMilli(),
				})
			}
			return true
		}
	}
	return false
}

// serveWakeDispatcher is the process-level dispatcher, installed at serve
// startup as part of the shared manager's observer chain (nil when
// background commands or wake are disabled). Stopped by shutdownServeBG.
var serveWakeDispatcher atomic.Pointer[wakeDispatcher]
