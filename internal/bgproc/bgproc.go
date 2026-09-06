// Package bgproc manages background processes started by the agent.
//
// The manager is deliberately process-scoped, not agent-scoped: agents are
// created per run (serve headless), per message (Telegram), and per REPL
// session, while background jobs must outlive the turn that started them.
// One Manager lives for the lifetime of the odek process; jobs are keyed by
// session id so concurrent sessions never see each other's jobs.
//
// Lifecycle contract (v1):
//   - A job runs until it exits, its timeout fires, or it is stopped.
//   - StopAll(session) runs at session teardown; Shutdown() at process exit.
//   - There is no detach mode: every job is killed when its session or the
//     process ends. Detached processes are an explicit non-goal (v1).
//
// Security contract:
//   - Output is captured into a bounded in-memory ring — never spilled to
//     disk, never persisted, never included in events (arg-hash discipline
//     lives in the events package).
//   - All addressing calls take the session id; a foreign session gets the
//     same "not found" answer as a stale id (no existence oracle).
//   - Stop signals the process GROUP (SIGTERM, then SIGKILL after a grace
//     window) so shell-spawned children die with the job; sandbox mode wraps
//     the command via the caller-supplied SandboxWrap (the shell tool's
//     pidfile mechanism) and runs the follow-up kill after a forced stop.
package bgproc

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Status is the lifecycle state of a background job.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited" // exit code 0
	StatusFailed  Status = "failed" // non-zero exit or external kill
	StatusTimeout Status = "timeout"
	StatusKilled  Status = "killed" // stopped via Stop/StopAll/Shutdown
)

const (
	// stopGrace is how long Stop waits after SIGTERM before SIGKILL.
	stopGrace = 5 * time.Second
	// maxTailBytes bounds the output tail carried in a Notice.
	maxTailBytes = 500
	// maxFinishedPerSession bounds memory held by finished job records.
	maxFinishedPerSession = 64
	// maxNoticesPerSession bounds the pending completion-notice queue.
	maxNoticesPerSession = 256
)

// ErrTooManyJobs is returned by Start when the session already has
// MaxJobsPerSession jobs running.
var ErrTooManyJobs = errors.New("background job limit reached for session")

// Job is a snapshot of a background job's state.
type Job struct {
	ID          string
	SessionID   string
	Command     string
	Status      Status
	ExitCode    int    // meaningful once ended (bash semantics; -1 when signaled)
	Err         string // terminal error detail, when applicable
	StartedAt   time.Time
	EndedAt     time.Time
	Timeout     time.Duration // 0 = until session end
	OutputBytes int64         // logical output size (dropped-front + retained); populated by Get snapshots
}

// Notice reports a job that reached a terminal state. It is delivered to the
// session's completion-notice queue (drained by the agent loop at the top of
// the next iteration) and to the Observer.
type Notice struct {
	JobID       string
	SessionID   string
	Command     string
	Status      Status
	ExitCode    int
	Duration    time.Duration
	OutputBytes int64
	Tail        string // last output, rune-boundary-safe
}

// Observer receives job lifecycle callbacks. Implementations must be
// non-blocking; they are invoked from the manager's waiter goroutines
// without the manager lock held.
type Observer interface {
	BGStarted(Job)
	BGExited(Notice)
}

// Config bounds the manager.
type Config struct {
	// MaxJobsPerSession caps concurrently running jobs per session.
	// <= 0 means unlimited (not recommended; callers should clamp).
	MaxJobsPerSession int
	// MaxOutputBytes caps the per-job output ring. <= 0 means 1 MiB.
	MaxOutputBytes int
	// MaxTimeout, when > 0, clamps the per-job timeout passed to Start.
	MaxTimeout time.Duration
	// SandboxWrap, when set, rewrites the command into a sandboxed argv
	// (e.g. the shell tool's docker-exec pidfile wrapper) and returns a
	// follow-up func invoked after the job is forcibly stopped. When nil,
	// commands run on the host via "sh -c".
	SandboxWrap func(command string) (argv []string, followUp func(), err error)
	// StripEnvNames, when non-empty, is removed from the child process
	// environment (host and sandbox-client processes alike; the operator
	// dangerous.strip_secrets_env_children knob feeds secrets.env names
	// here). Empty/nil inherits the parent environment unchanged.
	StripEnvNames []string
}

type jobEntry struct {
	job      Job
	reason   Status // pending terminal reason for a forced stop (killed/timeout)
	ring     outputRing
	stopping bool
	exited   chan struct{} // closed once by the waiter

	cmd      *exec.Cmd
	followUp func()
	timer    *time.Timer
}

// Manager owns every background job of the process.
type Manager struct {
	cfg  Config
	obs  Observer
	mu   sync.Mutex
	sess map[string][]*jobEntry // session -> jobs in creation order
	out  map[string][]Notice    // session -> pending completion notices
	seq  int
}

// NewManager returns a manager with the given bounds and observer (both may
// be zero/nil, applying defaults).
func NewManager(cfg Config, obs Observer) *Manager {
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 1 << 20
	}
	return &Manager{
		cfg:  cfg,
		obs:  obs,
		sess: make(map[string][]*jobEntry),
		out:  make(map[string][]Notice),
	}
}

// Start spawns command in the given session and returns immediately.
// timeout > 0 kills the job (StatusTimeout) when it elapses; 0 means the job
// runs until session end. The returned snapshot has Status running.
func (m *Manager) Start(sessionID, command, cwd string, timeout time.Duration) (*Job, error) {
	if sessionID == "" {
		return nil, errors.New("bgproc: empty session id")
	}
	if len(command) == 0 || isBlank(command) {
		return nil, errors.New("bgproc: empty command")
	}
	if m.cfg.MaxTimeout > 0 && timeout > m.cfg.MaxTimeout {
		timeout = m.cfg.MaxTimeout
	}
	if timeout < 0 {
		timeout = 0
	}

	cmd, followUp, err := m.buildCommand(command, cwd)
	if err != nil {
		return nil, fmt.Errorf("bgproc: %w", err)
	}

	m.mu.Lock()
	// Enforce the per-session concurrency cap under the lock so concurrent
	// Starts cannot race past it.
	running := 0
	for _, e := range m.sess[sessionID] {
		if e.job.Status == StatusRunning {
			running++
		}
	}
	if m.cfg.MaxJobsPerSession > 0 && running >= m.cfg.MaxJobsPerSession {
		m.mu.Unlock()
		return nil, ErrTooManyJobs
	}
	m.pruneLocked(sessionID)

	e := &jobEntry{
		job: Job{
			ID:        m.newIDLocked(),
			SessionID: sessionID,
			Command:   command,
			Status:    StatusRunning,
			StartedAt: time.Now().UTC(),
			Timeout:   timeout,
		},
		reason:   StatusRunning, // sentinel: no forced stop pending
		exited:   make(chan struct{}),
		cmd:      cmd,
		followUp: followUp,
	}
	e.ring.limit = m.cfg.MaxOutputBytes
	cmd.Stdout = &e.ring
	cmd.Stderr = &e.ring // interleaved, matching shell tool semantics
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("bgproc: spawn: %w", err)
	}
	m.sess[sessionID] = append(m.sess[sessionID], e)
	snapshot := e.job
	m.mu.Unlock()

	if timeout > 0 {
		e.timer = time.AfterFunc(timeout, func() { m.forceStop(sessionID, e, StatusTimeout) })
	}
	if m.obs != nil {
		m.obs.BGStarted(snapshot)
	}
	go m.wait(sessionID, e)
	return &snapshot, nil
}

// buildCommand assembles the exec.Cmd for host or sandbox mode.
func (m *Manager) buildCommand(command, cwd string) (*exec.Cmd, func(), error) {
	stripEnv := func(c *exec.Cmd) {
		if len(m.cfg.StripEnvNames) == 0 {
			return
		}
		strip := make(map[string]bool, len(m.cfg.StripEnvNames))
		for _, n := range m.cfg.StripEnvNames {
			strip[n] = true
		}
		env := os.Environ()
		out := make([]string, 0, len(env))
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			if !strip[name] {
				out = append(out, kv)
			}
		}
		c.Env = out
	}
	// Both host and sandbox children run as their own process-group leader
	// so Stop can tear the whole tree down with one group signal (mirrors
	// the shell tool's Setpgid semantics).
	attrs := &syscall.SysProcAttr{Setpgid: true}
	if m.cfg.SandboxWrap != nil {
		argv, followUp, err := m.cfg.SandboxWrap(command)
		if err != nil {
			return nil, nil, err
		}
		if len(argv) == 0 {
			return nil, nil, errors.New("sandbox wrapper returned empty argv")
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.SysProcAttr = attrs
		cmd.Dir = cwd
		cmd.WaitDelay = 3 * time.Second
		return cmd, followUp, nil
	}
	cmd := exec.Command("sh", "-c", command)
	stripEnv(cmd)
	cmd.SysProcAttr = attrs
	cmd.Dir = cwd
	cmd.WaitDelay = 3 * time.Second
	return cmd, nil, nil
}

// wait reaps the process and finalizes the job exactly once.
func (m *Manager) wait(sessionID string, e *jobEntry) {
	err := e.cmd.Wait()
	if e.timer != nil {
		e.timer.Stop()
	}

	m.mu.Lock()
	now := time.Now().UTC()
	e.job.EndedAt = now
	// ProcessState is authoritative once Wait returns. It survives
	// exec.ErrWaitDelay — a grandchild kept the output pipes open and the
	// WaitDelay drain timed out: output truncation, not process failure
	// (B3-TOOLS-2). It also outranks a pending killed/timeout reason
	// recorded by a Stop that raced a self-completing process.
	exitCode := -1
	if e.cmd.ProcessState != nil {
		exitCode = e.cmd.ProcessState.ExitCode()
	}
	switch {
	case exitCode >= 0:
		e.job.ExitCode = exitCode
		if exitCode == 0 {
			e.job.Status = StatusExited
		} else {
			e.job.Status = StatusFailed
			if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
				e.job.Err = err.Error()
			} else {
				e.job.Err = fmt.Sprintf("exit status %d", exitCode)
			}
		}
	case e.reason == StatusKilled || e.reason == StatusTimeout:
		e.job.Status = e.reason
	case err == nil:
		// Unreachable in practice (a nil Wait error implies a completed
		// exit); kept as a fail-safe.
		e.job.Status = StatusExited
		e.job.ExitCode = 0
	default:
		e.job.Status = StatusFailed
		if ee, ok := err.(*exec.ExitError); ok {
			e.job.ExitCode = ee.ExitCode()
		} else {
			e.job.ExitCode = -1
		}
		e.job.Err = err.Error()
	}
	notice := Notice{
		JobID:       e.job.ID,
		SessionID:   sessionID,
		Command:     e.job.Command,
		Status:      e.job.Status,
		ExitCode:    e.job.ExitCode,
		Duration:    now.Sub(e.job.StartedAt),
		OutputBytes: e.ring.dropped + int64(len(e.ring.buf)),
		Tail:        e.ring.tail(maxTailBytes),
	}
	m.enqueueNoticeLocked(sessionID, notice)
	obs := m.obs
	m.mu.Unlock()

	if e.reason == StatusKilled || e.reason == StatusTimeout {
		m.runFollowUp(e)
	}
	close(e.exited)
	if obs != nil {
		obs.BGExited(notice)
	}
}

// runFollowUp invokes the sandbox follow-up kill (best-effort, bounded by the
// wrapper's own timeout — see shell.go's sandboxKillFollowupTimeout).
func (m *Manager) runFollowUp(e *jobEntry) {
	if e.followUp != nil {
		e.followUp()
	}
}

// forceStop signals the process group and records the pending terminal
// reason. Used by Stop (killed) and the timeout timer (timeout).
func (m *Manager) forceStop(sessionID string, e *jobEntry, reason Status) {
	m.mu.Lock()
	if e.job.Status != StatusRunning || e.stopping {
		m.mu.Unlock()
		return
	}
	e.stopping = true
	e.reason = reason
	pid := e.cmd.Process.Pid
	m.mu.Unlock()

	// Signal the whole group (negative pid) so shell-spawned children die
	// with the job. If the group does not exist (child already reaped, or
	// a platform where setpgid did not apply), fall back to the direct pid.
	signal := func(sig syscall.Signal) {
		if err := syscall.Kill(-pid, sig); err != nil {
			_ = syscall.Kill(pid, sig)
		}
	}
	signal(syscall.SIGTERM)
	go func() {
		select {
		case <-e.exited:
		case <-time.After(stopGrace):
			signal(syscall.SIGKILL)
		}
	}()
}

// Stop stops the job if it is running and owned by sessionID. It returns the
// job snapshot and whether the job was found under that session. Stopping an
// already-finished job is not an error and does not change its status.
func (m *Manager) Stop(sessionID, jobID string) (Job, bool) {
	m.mu.Lock()
	e := m.findLocked(sessionID, jobID)
	if e == nil {
		m.mu.Unlock()
		return Job{}, false
	}
	running := e.job.Status == StatusRunning
	m.mu.Unlock()

	if running {
		m.forceStop(sessionID, e, StatusKilled)
		// Wait for the reaper so callers see the terminal status —
		// bg_stop reports "killed", not "still running". WaitDelay (3s)
		// bounds cmd.Wait once the process is gone; the safety net here
		// covers the SIGTERM grace window plus margin.
		select {
		case <-e.exited:
		case <-time.After(stopGrace + 4*time.Second):
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return e.job, true
}

// StopAll stops every running job of the session and returns the killed
// jobs. Jobs of other sessions are untouched.
func (m *Manager) StopAll(sessionID string) []Job {
	return m.stopMany(func(e *jobEntry) bool { return true }, sessionID)
}

// Shutdown stops every running job across all sessions (process exit).
func (m *Manager) Shutdown() []Job {
	var all []Job
	m.mu.Lock()
	sessions := make([]string, 0, len(m.sess))
	for s := range m.sess {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		all = append(all, m.StopAll(s)...)
	}
	return all
}

// stopMany stops running jobs of sessionID matched by the entry filter.
func (m *Manager) stopMany(match func(*jobEntry) bool, sessionID string) []Job {
	m.mu.Lock()
	var targets []*jobEntry
	for _, e := range m.sess[sessionID] {
		if e.job.Status == StatusRunning && !e.stopping {
			targets = append(targets, e)
		}
	}
	m.mu.Unlock()

	var killed []Job
	for _, e := range targets {
		if match(e) {
			if j, ok := m.Stop(sessionID, e.job.ID); ok {
				killed = append(killed, j)
			}
		}
	}
	return killed
}

// List returns a snapshot of the session's jobs in creation order.
func (m *Manager) List(sessionID string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.sess[sessionID]))
	for _, e := range m.sess[sessionID] {
		out = append(out, e.job)
	}
	return out
}

// Get returns the job snapshot; ok is false for unknown or foreign ids.
// The snapshot carries the job's logical output size so callers can judge
// whether reading the output is worthwhile (B3-TOOLS-3).
func (m *Manager) Get(sessionID, jobID string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(sessionID, jobID)
	if e == nil {
		return Job{}, false
	}
	job := e.job
	job.OutputBytes = e.ring.size() // ring lock nested under m.mu; no reverse order exists
	return job, true
}

// Output returns up to limit bytes of output recorded after the absolute
// byte offset since (limit <= 0 = no cap), plus the cursor for the next
// read. When the ring has dropped bytes from the front, a truncation marker
// precedes the retained window. Foreign or unknown ids yield an error
// identical in shape for both cases.
func (m *Manager) Output(sessionID, jobID string, since int64, limit int) (string, int64, error) {
	if since < 0 {
		since = 0
	}
	m.mu.Lock()
	e := m.findLocked(sessionID, jobID)
	if e == nil {
		m.mu.Unlock()
		return "", 0, fmt.Errorf("unknown background job %q", jobID)
	}
	m.mu.Unlock()

	chunk, next := e.ring.readFrom(since, limit)
	return chunk, next, nil
}

// DrainNotices pops all pending completion notices for the session.
func (m *Manager) DrainNotices(sessionID string) []Notice {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.out[sessionID]
	delete(m.out, sessionID)
	return n
}

// ── internal helpers ─────────────────────────────────────────────────────

func (m *Manager) findLocked(sessionID, jobID string) *jobEntry {
	for _, e := range m.sess[sessionID] {
		if e.job.ID == jobID {
			return e
		}
	}
	return nil
}

func (m *Manager) newIDLocked() string {
	for {
		m.seq++
		id := fmt.Sprintf("bg_%08x", m.seq)
		if m.seq == 1<<31 {
			m.seq = 0
		}
		dup := false
		for _, jobs := range m.sess {
			for _, e := range jobs {
				if e.job.ID == id {
					dup = true
					break
				}
			}
			if dup {
				break
			}
		}
		if !dup {
			return id
		}
	}
}

// pruneLocked drops the oldest finished jobs of the session when the record
// count exceeds the finished cap plus the concurrency cap. Caller holds mu.
func (m *Manager) pruneLocked(sessionID string) {
	jobs := m.sess[sessionID]
	limit := maxFinishedPerSession + m.cfg.MaxJobsPerSession
	if limit <= 0 {
		limit = maxFinishedPerSession
	}
	for len(jobs) > limit {
		idx := -1
		for i, e := range jobs {
			if e.job.Status != StatusRunning {
				idx = i
				break
			}
		}
		if idx < 0 {
			return // all running; the concurrency cap will gate Starts
		}
		jobs = append(jobs[:idx], jobs[idx+1:]...)
	}
	m.sess[sessionID] = jobs
}

func (m *Manager) enqueueNoticeLocked(sessionID string, n Notice) {
	q := append(m.out[sessionID], n)
	if len(q) > maxNoticesPerSession {
		q = q[len(q)-maxNoticesPerSession:]
	}
	m.out[sessionID] = q
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// ── output ring ──────────────────────────────────────────────────────────

// outputRing is a WriteCloser-free bounded byte ring that keeps the most
// recent output: once the cap is hit, the OLDEST bytes are dropped from the
// front and counted, so readers always see the freshest window plus an
// explicit marker for what was dropped. Front-dropping steps back at most
// utf8.UTFMax-1 bytes to a rune boundary, so valid multibyte characters
// are never split; binary (invalid UTF-8) output has no boundary, and the
// byte cap stays authoritative — the ring is bounded unconditionally.
type outputRing struct {
	mu      sync.Mutex
	buf     []byte
	limit   int
	dropped int64
}

func (r *outputRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		cut := len(r.buf) - r.limit
		// Walk back at most utf8.UTFMax-1 bytes to a rune boundary so a
		// multibyte character is never split. Invalid UTF-8 (binary
		// output) has no boundary — the cap is unconditional, so cut
		// where the arithmetic lands rather than growing the ring
		// unbounded (B3-TOOLS-1).
		for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(r.buf[cut]); i++ {
			cut--
		}
		r.dropped += int64(cut)
		r.buf = r.buf[cut:]
	}
	return len(p), nil
}

// size returns the logical output size: bytes dropped from the front of
// the ring plus the retained window.
func (r *outputRing) size() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped + int64(len(r.buf))
}

// readFrom returns up to limit bytes (int: a buffer size, bounded at the
// caller) of output after absolute offset since, plus the cursor for the
// next read. The cursor is an absolute offset into the logical stream:
// marker bytes are not counted.
func (r *outputRing) readFrom(since int64, limit int) (string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.dropped + int64(len(r.buf))
	if since >= end {
		return "", end
	}
	start := since
	marker := ""
	if start < r.dropped {
		marker = fmt.Sprintf("... [%d earlier output bytes truncated]\n", r.dropped)
		start = r.dropped
	}
	// Explicit bound checks keep the int64→int conversion provably
	// bounded (overflow-safe on 32-bit platforms).
	rel := start - r.dropped
	if rel < 0 {
		rel = 0
	}
	if rel > int64(len(r.buf)) {
		rel = int64(len(r.buf))
	}
	if rel > math.MaxInt32 {
		rel = math.MaxInt32
	}
	window := r.buf[int(rel):]
	if limit > 0 && len(window) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(window[cut]) {
			cut--
		}
		window = window[:cut]
	}
	return marker + string(window), start + int64(len(window))
}

// tail returns the last n bytes of output, cut forward to a rune boundary.
func (r *outputRing) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == 0 {
		return ""
	}
	if len(r.buf) <= n {
		return string(r.buf)
	}
	cut := len(r.buf) - n
	for cut < len(r.buf) && !utf8.RuneStart(r.buf[cut]) {
		cut++
	}
	return string(r.buf[cut:])
}
