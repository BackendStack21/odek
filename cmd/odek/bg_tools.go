package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/redact"
)

// bgOutputChunkBytes caps a single bg_output result.
const bgOutputChunkBytes = 32 << 10

// bgCommandHead caps the command echo inside list/status results.
const bgCommandHead = 80

// BackgroundSettings carries the resolved `background` config section as
// primitives, decoupled from the config package's types.
type BackgroundSettings struct {
	Enabled           bool
	MaxJobs           int
	MaxOutputBytes    int
	MaxTimeoutSeconds int  // 0 = uncapped (jobs bounded by session lifetime)
	Notify            bool // background.notify == "observe"

	// StripChildSecretEnv: operator opt-in (dangerous.strip_secrets_env_children)
	// — host-mode job children get secrets.env names stripped from their env.
	StripChildSecretEnv bool

	// Wake-on-complete (serve surface only — see cmd/odek/bg_wake.go).
	WakeOnComplete  bool
	WakeCoalesceMS  int
	MaxWakesPerHour int
}

// bgRuntime binds the process-scoped manager to one agent session.
// The manager must be created once per process (serve/Telegram host many
// sessions); each construction site passes its own session id.
type bgRuntime struct {
	mgr      *bgproc.Manager
	session  string
	provider func() string // engine notice provider (drain-once)

	// container is the sandbox container name, readable after construction
	// (surfaces that start the sandbox after building tools bind it late,
	// before the agent runs — jobs only spawn once the agent iterates).
	container atomic.Value
}

// backgroundSettingsFromResolved maps the resolved background config onto
// the runtime settings primitives.
func backgroundSettingsFromResolved(resolved config.ResolvedConfig) BackgroundSettings {
	b := resolved.Background
	return BackgroundSettings{
		Enabled:           b.Enabled,
		MaxJobs:           b.MaxJobs,
		MaxOutputBytes:    b.MaxOutputBytes,
		MaxTimeoutSeconds: b.MaxTimeoutSeconds,
		Notify:            b.Notify == "observe",
		StripChildSecretEnv: resolved.Dangerous.StripSecretsEnvChildrenEnabled(),
	}
}

// newBackgroundRuntime builds the runtime for one surface. Returns nil when
// background commands are disabled or the session id is empty — callers then
// simply skip tool registration (the tools stay absent from the registry).
//
// emit, when non-nil, receives bg_started / bg_exited runtime events
// (command content is never included — only its SHA-256). extraObs, when
// given, receives the same lifecycle callbacks (e.g. the Telegram chat
// pusher) alongside the event observer.
func newBackgroundRuntime(s BackgroundSettings, sessionID, containerName string, emit func(events.Event), extraObs ...bgproc.Observer) *bgRuntime {
	if !s.Enabled || sessionID == "" {
		return nil
	}
	if s.MaxJobs <= 0 {
		s.MaxJobs = 8
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = 1 << 20
	}
	cfg := bgproc.Config{
		MaxJobsPerSession: s.MaxJobs,
		MaxOutputBytes:    s.MaxOutputBytes,
	}
	if s.MaxTimeoutSeconds > 0 {
		cfg.MaxTimeout = time.Duration(s.MaxTimeoutSeconds) * time.Second
	}
	if s.StripChildSecretEnv {
		cfg.StripEnvNames = secretsEnvNames()
	}
	rt := &bgRuntime{session: sessionID}
	if containerName != "" {
		rt.container.Store(containerName)
	}
	// The wrap resolves the container per spawn so late binding works
	// (surfaces that start the sandbox after building tools call
	// SetContainer before the agent runs). Empty name = host mode, the
	// same default the manager uses without a wrapper.
	cfg.SandboxWrap = func(command string) ([]string, func(), error) {
		name, _ := rt.container.Load().(string)
		if name == "" {
			return []string{"sh", "-c", command}, nil, nil
		}
		argv, followUp := wrapSandboxCommand(name, command)
		return argv, followUp, nil
	}
	var obs bgproc.Observer
	if emit != nil {
		obs = &bgEventObserver{emit: emit}
	}
	if len(extraObs) > 0 {
		list := make([]bgproc.Observer, 0, len(extraObs)+1)
		if obs != nil {
			list = append(list, obs)
		}
		list = append(list, extraObs...)
		obs = &bgObserverGroup{list: list}
	}
	if obs != nil {
		rt.mgr = bgproc.NewManager(cfg, obs)
	} else {
		rt.mgr = bgproc.NewManager(cfg, nil)
	}
	rt.provider = func() string {
		return rt.formatNotices(rt.mgr.DrainNotices(sessionID))
	}
	return rt
}

// SetContainer binds (or rebinds) the sandbox container after construction.
// Surfaces that start the sandbox after building tools call this before the
// agent runs. "" selects host mode.
func (rt *bgRuntime) SetContainer(name string) {
	if rt == nil {
		return
	}
	rt.container.Store(name)
}

// Shutdown kills every running job of the process. Deferred at each surface
// exit (REPL quit, run end, serve/Telegram/schedule shutdown, sub-agent exit).
func (rt *bgRuntime) Shutdown() []bgproc.Job {
	if rt == nil || rt.mgr == nil {
		return nil
	}
	return rt.mgr.Shutdown()
}

// StopAll kills every running job of the runtime's session.
func (rt *bgRuntime) StopAll() []bgproc.Job {
	if rt == nil || rt.mgr == nil {
		return nil
	}
	return rt.mgr.StopAll(rt.session)
}

// formatNotices renders drained completion notices into the observe-phase
// message. The whole block crosses the trust boundary (job output is hostile
// even when the spawn was approved), so it is untrusted-wrapped; tails are
// redacted before entering the context.
func (rt *bgRuntime) formatNotices(notices []bgproc.Notice) string {
	plain := rt.formatNoticesPlain(notices)
	if plain == "" {
		return ""
	}
	return wrapUntrusted(context.Background(), "bg", plain)
}

// formatNoticesPlain renders notices for any consumer. The model context
// gets the untrusted-wrapped variant (formatNotices); humans get this plain
// text. Tails are redacted; the manager already clamped them.
func (rt *bgRuntime) formatNoticesPlain(notices []bgproc.Notice) string {
	if len(notices) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[bg] %d background job(s) finished since last check:", len(notices))
	for _, n := range notices {
		if line := formatOneNotice(n); line != "" {
			b.WriteString("\n- ")
			b.WriteString(line)
		}
	}
	b.WriteString("\nFull output: bg_output(job_id, since). Status: bg_status(job_id). Stop: bg_stop(job_id).")
	return b.String()
}

// formatOneNotice renders a single job-exit line (redacted tail included).
func formatOneNotice(n bgproc.Notice) string {
	dur := n.Duration.Round(time.Second)
	var line string
	switch n.Status {
	case bgproc.StatusExited:
		line = fmt.Sprintf("%s `%s` → exited (code %d) after %s", n.JobID, headString(n.Command, bgCommandHead), n.ExitCode, dur)
	case bgproc.StatusFailed:
		line = fmt.Sprintf("%s `%s` → failed (exit %d) after %s", n.JobID, headString(n.Command, bgCommandHead), n.ExitCode, dur)
	case bgproc.StatusTimeout:
		line = fmt.Sprintf("%s `%s` → timeout after %s", n.JobID, headString(n.Command, bgCommandHead), dur)
	default:
		line = fmt.Sprintf("%s `%s` → %s after %s", n.JobID, headString(n.Command, bgCommandHead), n.Status, dur)
	}
	if n.Tail != "" {
		line += "\n  tail: " + redact.RedactSecrets(n.Tail)
	}
	return line
}

// appendBackgroundTools registers the five bg_* tools when the runtime is
// active. bg_output is wrapped with untrustedToolWrapper so job output is
// nonce-wrapped and audit-recorded like every other external content.
func appendBackgroundTools(tools []odek.Tool, rt *bgRuntime) []odek.Tool {
	if rt == nil || rt.mgr == nil {
		return tools
	}
	// bg_start reuses the shell tool's approval gate (fail-closed: no shell
	// tool in the registry → no bg_start).
	var shell *shellTool
	for _, t := range tools {
		if st, ok := t.(*shellTool); ok {
			shell = st
			break
		}
	}
	if shell == nil {
		return tools
	}
	return append(tools,
		&bgStartTool{rt: rt, shell: shell},
		&bgListTool{rt: rt},
		&bgStatusTool{rt: rt},
		&untrustedToolWrapper{inner: &bgOutputTool{rt: rt}, source: "bg_output"},
		&bgStopTool{rt: rt},
	)
}

// ── event observer ───────────────────────────────────────────────────────

// bgEventObserver bridges job lifecycle to the runtime event stream.
// Command content never leaves the process: events carry its SHA-256 only.
type bgEventObserver struct {
	emit func(events.Event)
}

func (o *bgEventObserver) BGStarted(j bgproc.Job) {
	o.emit(events.Event{
		Schema:    "odek.event/v1",
		Type:      "bg_started",
		SessionID: j.SessionID,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"job_id":         j.ID,
			"command_sha256": sha256Hex(j.Command),
			"timeout_s":      int(j.Timeout.Seconds()),
		},
	})
}

func (o *bgEventObserver) BGExited(n bgproc.Notice) {
	o.emit(events.Event{
		Schema:    "odek.event/v1",
		Type:      "bg_exited",
		SessionID: n.SessionID,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"job_id":       n.JobID,
			"status":       string(n.Status),
			"exit_code":    n.ExitCode,
			"duration_ms":  n.Duration.Milliseconds(),
			"output_bytes": n.OutputBytes,
		},
	})
}

// bgObserverGroup fans lifecycle callbacks out to several observers.
type bgObserverGroup struct {
	list []bgproc.Observer
}

func (g *bgObserverGroup) BGStarted(j bgproc.Job) {
	for _, o := range g.list {
		if o != nil {
			o.BGStarted(j)
		}
	}
}

func (g *bgObserverGroup) BGExited(n bgproc.Notice) {
	for _, o := range g.list {
		if o != nil {
			o.BGExited(n)
		}
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func headString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ── tools ────────────────────────────────────────────────────────────────

type bgStartTool struct {
	rt    *bgRuntime
	shell *shellTool // approval gate — shell parity, set by appendBackgroundTools
}

func (t *bgStartTool) Name() string { return "bg_start" }

func (t *bgStartTool) Description() string {
	return `Start a shell command in the background and return immediately.
Use for long-running work: builds, full test suites, dev servers, watchers, fuzz runs, batch jobs.
The command runs detached from the conversation: you keep working while it
runs. Completion is delivered automatically: the exit notice is injected into
a later iteration of a running turn, and when the turn has ended the client
wakes the session on job completion (or the notice is delivered on the next
turn). Never call sleep or otherwise pause to wait for a job — it blocks the
loop for nothing. Poll with bg_status / bg_output only if notices are
disabled or your current turn depends on the result.
timeout_seconds: optional kill timer; 0 or absent = run until session end
(operator cap may clamp explicit values). Jobs are killed when the session
or the process ends. Output is capped; retrieve it with bg_output.`
}

func (t *bgStartTool) Schema() any {
	return map[string]any{
		"type":     "object",
		"required": []string{"command"},
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to run in the background",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Kill timer in seconds; 0/absent = until session end",
			},
		},
	}
}

func (t *bgStartTool) Call(args string) (string, error) {
	var p struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Command) == "" {
		return "", fmt.Errorf("bg_start requires a non-empty \"command\"")
	}
	// Spawn-time approval, shell parity: the loop's batch gate only covers
	// multi-call batches, so — exactly like shellTool — bg_start gates
	// itself here (allowlist/denylist, unread-exec script gate, approver).
	if t.shell == nil {
		return "", fmt.Errorf("bg_start unavailable: approval gate not wired")
	}
	if err := t.shell.checkApproval(p.Command, "background job"); err != nil {
		return "", err
	}
	job, err := t.rt.mgr.Start(t.rt.session, p.Command, "", time.Duration(p.TimeoutSeconds)*time.Second)
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"job_id": job.ID, "status": string(job.Status)})
	return string(out), nil
}

type bgListTool struct{ rt *bgRuntime }

func (t *bgListTool) Name() string { return "bg_list" }
func (t *bgListTool) Description() string {
	return "List this session's background jobs with id, status, runtime, and exit code. Never sleep-wait for a job — completion is delivered automatically (see bg_start)."
}
func (t *bgListTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *bgListTool) Call(args string) (string, error) {
	jobs := t.rt.mgr.List(t.rt.session)
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		entry := map[string]any{
			"job_id":    j.ID,
			"command":   headString(j.Command, bgCommandHead),
			"status":    string(j.Status),
			"runtime_s": jobRuntimeSeconds(j),
		}
		if j.Status != bgproc.StatusRunning {
			entry["exit_code"] = j.ExitCode
		}
		out = append(out, entry)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type bgStatusTool struct{ rt *bgRuntime }

func (t *bgStatusTool) Name() string { return "bg_status" }

func (t *bgStatusTool) Description() string {
	return `Get the status of one background job: running/exited/failed/timeout/killed,
exit code, duration, and output size. Returns {"status":"unknown"} for ids
that never existed, were started by another session, died with a restart,
or whose finished record was evicted (the oldest finished jobs are pruned
when the per-session record cap is exceeded). Never sleep-wait for a job —
completion is delivered automatically (see bg_start).`
}

func (t *bgStatusTool) Schema() any {
	return map[string]any{
		"type":     "object",
		"required": []string{"job_id"},
		"properties": map[string]any{
			"job_id": map[string]any{"type": "string", "description": "Job id from bg_start"},
		},
	}
}

func (t *bgStatusTool) Call(args string) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.JobID == "" {
		return "", fmt.Errorf("bg_status requires \"job_id\"")
	}
	job, ok := t.rt.mgr.Get(t.rt.session, p.JobID)
	if !ok {
		b, _ := json.Marshal(map[string]any{"job_id": p.JobID, "status": "unknown"})
		return string(b), nil
	}
	entry := map[string]any{
		"job_id":       job.ID,
		"status":       string(job.Status),
		"runtime_s":    jobRuntimeSeconds(job),
		"output_bytes": job.OutputBytes,
	}
	if job.Status != bgproc.StatusRunning {
		entry["exit_code"] = job.ExitCode
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type bgOutputTool struct{ rt *bgRuntime }

func (t *bgOutputTool) Name() string { return "bg_output" }

func (t *bgOutputTool) Description() string {
	return `Read a background job's captured output (stdout+stderr interleaved).
Pass the returned next_cursor as since to continue reading; output older
than the ring buffer is marked as truncated. Chunks are capped at 32 KiB.
Only the job's own session can read it. Never sleep-wait for a job —
completion is delivered automatically (see bg_start).`
}

func (t *bgOutputTool) Schema() any {
	return map[string]any{
		"type":     "object",
		"required": []string{"job_id"},
		"properties": map[string]any{
			"job_id": map[string]any{"type": "string", "description": "Job id from bg_start"},
			"since":  map[string]any{"type": "integer", "minimum": 0, "description": "Cursor from the previous read; 0/absent = start"},
		},
	}
}

func (t *bgOutputTool) Call(args string) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
		Since int64  `json:"since"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.JobID == "" {
		return "", fmt.Errorf("bg_output requires \"job_id\"")
	}
	chunk, next, err := t.rt.mgr.Output(t.rt.session, p.JobID, p.Since, bgOutputChunkBytes)
	if err != nil {
		b, _ := json.Marshal(map[string]any{"job_id": p.JobID, "status": "unknown"})
		return string(b), nil
	}
	out, _ := json.Marshal(map[string]any{"job_id": p.JobID, "output": chunk, "next_cursor": next})
	return string(out), nil
}

type bgStopTool struct{ rt *bgRuntime }

func (t *bgStopTool) Name() string { return "bg_stop" }

func (t *bgStopTool) Description() string {
	return `Stop a background job (SIGTERM to the process group, SIGKILL after a
grace window). Returns the job's terminal status; stopping an
already-finished job is not an error. Never sleep-wait for a job —
completion is delivered automatically (see bg_start).`
}

func (t *bgStopTool) Schema() any {
	return map[string]any{
		"type":     "object",
		"required": []string{"job_id"},
		"properties": map[string]any{
			"job_id": map[string]any{"type": "string", "description": "Job id from bg_start"},
		},
	}
}

func (t *bgStopTool) Call(args string) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.JobID == "" {
		return "", fmt.Errorf("bg_stop requires \"job_id\"")
	}
	job, ok := t.rt.mgr.Stop(t.rt.session, p.JobID)
	if !ok {
		b, _ := json.Marshal(map[string]any{"job_id": p.JobID, "status": "unknown"})
		return string(b), nil
	}
	out, _ := json.Marshal(map[string]any{"job_id": job.ID, "status": string(job.Status)})
	return string(out), nil
}

// jobRuntimeSeconds is the job's elapsed (or total) runtime in seconds.
func jobRuntimeSeconds(j bgproc.Job) float64 {
	end := j.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(j.StartedAt).Seconds()
}
