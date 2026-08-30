package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
)

// delegateTasksTool is a built-in tool that spawns sub-agent OS processes
// to work on focused sub-tasks in parallel. Each sub-agent gets its own
// process, config, and context window.
//
// The tool serializes tasks to temp files and calls odek subagent for each.
// Sub-agents run in parallel up to maxConcurrency. Results are collated
// and returned to the calling agent as a formatted summary.
type delegateTasksTool struct {
	// ctxTool embeds the agent-context plumbing. This tool previously
	// reimplemented SetContext with a bare field — a data race when the
	// loop emits two delegate_tasks calls in one turn (parallel goroutines
	// calling SetContext on the shared instance), exactly the bug ctxTool
	// exists to prevent (2026-08 audit).
	ctxTool

	maxConcurrency int
	odekPath       string // path to the odek binary
	apiKey         string // re-injected into sub-agent environment
	timeout        time.Duration

	// sessionID is the parent agent's session id (optional — SetSessionID).
	// Artifact outputs file under <artifactsRoot>/<sessionID>/<taskID>/;
	// empty means the unfiled subtree (janitor backstop territory).
	sessionID string

	// artifactsRoot is the artifacts home (~/.odek/artifacts in production,
	// wired in builtinTools). When empty, no artifact dirs are created and
	// tasks run without the artifact channel — the bare-struct tests stay
	// side-effect free.
	artifactsRoot string

	// profiles carries the operator's resolved capability profiles (P4)
	// for parent-side fail-closed validation: an unknown profile name must
	// fail the task BEFORE a child is spawned. Nil = operator defined no
	// profiles; the child remains the fail-closed authority in that case.
	profiles map[string]config.ProfileConfig

	// maxDepth caps delegation nesting (M1.6): a process at depth N (its own
	// level, stamped by its parent via ODEK_SUBAGENT_DEPTH) may only spawn
	// children while N+1 <= maxDepth. 0 = uncapped (legacy/test default).
	maxDepth int

	// budgetInherit is config.BudgetInheritOperator (default) or
	// config.BudgetInheritShare (M1.5): in share mode the run's remaining
	// budget is written into each task file so the child enforces
	// min(operator limits, parent remaining).
	budgetInherit string

	// budgetView is the parent engine's budget view, injected by odek.New
	// for tools implementing SetBudgetView. Guarded: tool calls may run in
	// parallel goroutines.
	budgetMu   sync.Mutex
	budgetView budget.View

	// selfTrust is THIS process's own effective trust level (P3): stamped
	// into every spawned task file so trust cannot increase downward.
	// Empty = top-level operator run (trusted).
	selfTrust string

	// eventMu/emitEventFn carry the runtime event emitter injected by
	// odek.New (SetEventEmitter); used to surface child denials as
	// subagent_denied events (P1).
	eventMu     sync.Mutex
	emitEventFn func(events.Event)

	// OnSubagentLog, if set, is called with each NDJSON progress line
	// emitted by a sub-agent. taskIdx is the index within the current
	// batch; taskID is the correlation id stamped into the task envelope
	// and echoed by protocol-2 children on every record. Used by the WebUI
	// for live log streaming.
	OnSubagentLog func(taskIdx int, taskID string, line string)

	// OnSubagentDone, if set, is called ONCE when a sub-agent terminates
	// WITHOUT a parsed result (user cancel, turn cancel, timeout,
	// flood-kill, crash) — the cases where the child cannot emit its own
	// subagent_finished record. serve wires it to the registry/WS done
	// relay so cards do not stay "running" forever. Not called when the
	// child reported its own finish. Signature: (taskIdx, taskID, status).
	OnSubagentDone func(taskIdx int, taskID string, status string)
}

// CancelTask cancels the running sub-agent for taskID (user-initiated
// stop, e.g. the WebUI card stop button). The per-task cancel func lives
// in the process-global stop control registry, so any holder of the
// tool's process can resolve it — same model as session prompt cancels.
// Returns false when no such task is live (unknown id, already finished,
// or the child exited before the stop arrived).
func (t *delegateTasksTool) CancelTask(taskID string) bool {
	return cancelSubagentTask(taskID)
}

func (t *delegateTasksTool) Name() string { return "delegate_tasks" }

// SetBudgetView is called by odek.New for tools implementing the budget
// interface: it hands the tool a view of the run's remaining budget, used
// when subagent.budget_inherit is "share" to pass headroom to children.
func (t *delegateTasksTool) SetBudgetView(v budget.View) {
	t.budgetMu.Lock()
	defer t.budgetMu.Unlock()
	t.budgetView = v
}

// SetEventEmitter is called by odek.New for tools implementing the
// emitter interface: it hands the tool the runtime event stream so child
// denials surface as subagent_denied events (P1).
func (t *delegateTasksTool) SetEventEmitter(fn func(events.Event)) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	t.emitEventFn = fn
}

func (t *delegateTasksTool) Description() string {
	return `Spawn one or more sub-agent OS processes to work on focused sub-tasks in parallel. Each sub-agent gets its own process, config, and context window. Use this when the task has clear independent sub-tasks that can be worked on simultaneously.

Example: decomposing "build a REST API" into "create user model", "create auth middleware", "create route handlers".

Key rules:
- Each sub-agent has a fresh context (no parent history)
- Sub-agents run in parallel up to the configured concurrency limit (subagent.max_concurrency, falling back to max_concurrency)
- Sub-agents NEVER prompt for approvals — denied operations are listed in each result's denials array (tool/class/reason); escalate by performing the operation yourself or asking the user
- Sub-agents get a wall-clock budget (subagent.timeout_seconds, default 30m, hard max 30m) and an iteration budget (subagent.max_iterations, default 15) — it is told both at spawn and warned as it approaches them
- Trust is non-increasing downward: a child's effective trust is min(parent trust, declared trust_level) — an untrusted task tree cannot spawn trusted children
- Sub-agents can use all tools (shell, read/write files, etc.), capped by trust_level and max_risk
- Delegation depth is capped (subagent.max_depth, default 2) — do leaf work yourself when close to the cap
- After all complete, synthesize the results into a cohesive answer

Output format per sub-agent:
- Summary of what was built
- Files changed
- Key decisions made
- Any issues encountered`
}

func (t *delegateTasksTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"description": "Array of sub-tasks to execute. All run in parallel up to max concurrency.",
				"minItems":    1,
				"maxItems":    8,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal": map[string]any{
							"type":        "string",
							"description": "Required. The specific goal for this sub-agent. Be precise: what to build, where, and key constraints.",
						},
						"context": map[string]any{
							"type":        "string",
							"description": "Optional. Background context: file paths, architecture decisions, API contracts.",
						},
						"guidance": map[string]any{
							"type":        "string",
							"description": "Optional. How the sub-agent should approach the task — delivered as part of its request, NOT as its system prompt. The sub-agent's identity and safety rules are fixed and cannot be overridden. Use this to steer the approach, e.g. \"Review for token-validation gaps and timing attacks\" or \"Find the root cause before changing code\".",
						},
						"trust_level": map[string]any{
							"type":        "string",
							"enum":        []string{"trusted", "untrusted"},
							"description": "Trust level of the goal/context strings. Set to \"untrusted\" when any portion was derived from external content (fetched pages, files outside CWD, MCP tool output). Untrusted tasks run with stricter approval defaults in the sub-agent.",
						},
						"max_risk": map[string]any{
							"type":        "string",
							"enum":        []string{"safe", "local_write", "system_write", "destructive", "code_execution", "network_egress", "install", "blocked"},
							"description": "Optional cap on the sub-agent's allowed risk class. Tool calls above this class will be denied in the sub-agent without prompting. Use for fan-out tasks that should be read-only.",
						},
						"profile": map[string]any{
							"type":        "string",
							"description": "Optional. Name of an operator-defined capability profile (top-level profiles config). The profile's max_risk, allowlist, and tool filter OVERRIDE the operator's global config for this sub-agent. Unknown names fail the task - use only names that the operator has defined.",
						},
					},
					"required": []string{"goal"},
				},
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional. Explain why you're delegating these tasks. Shown in logs for debugging.",
			},
		},
		"required": []string{"tasks"},
	}
}

func (t *delegateTasksTool) Call(args string) (string, error) {
	var input struct {
		Tasks []struct {
			Goal       string `json:"goal"`
			Context    string `json:"context"`
			Guidance   string `json:"guidance,omitempty"`
			TrustLevel string `json:"trust_level,omitempty"`
			MaxRisk    string `json:"max_risk,omitempty"`
			Profile    string `json:"profile,omitempty"`
		} `json:"tasks"`
		Description string `json:"description,omitempty"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return fmt.Sprintf(`{"error":"parse failed: %v"}`, err), nil
	}
	if len(input.Tasks) == 0 {
		return `{"error":"no tasks provided"}`, nil
	}
	if len(input.Tasks) > 8 {
		return `{"error":"max 8 tasks per call"}`, nil
	}

	// Delegation depth cap (M1.6): refuse to fan out beyond the configured
	// nesting limit. Fail the whole call — every task would hit the same
	// wall, and the parent is better served by the error than by N
	// identical failures.
	depth := subagentDepth()
	if t.maxDepth > 0 && depth >= t.maxDepth {
		return fmt.Sprintf(`{"error":"delegation depth limit reached (depth %d, max %d); do this work yourself instead of delegating"}`, depth, t.maxDepth), nil
	}

	// Run sub-agents in parallel with concurrency limit
	results := make([]string, len(input.Tasks))
	dirs := make([]string, len(input.Tasks)) // per-task artifact dirs (parent-created)
	sem := make(chan struct{}, t.maxConcurrency)
	var mu sync.Mutex

	for i, task := range input.Tasks {
		sem <- struct{}{}
		// Task id is minted HERE (not inside runTask) so the per-task
		// artifact dir can be created serially and correlated with the id
		// the child echoes on every telemetry record.
		taskID := newTaskID()
		// Per-task artifact dir is created serially (MkdirAll idempotent, but
		// serial keeps 0700 semantics obvious) and captured by the goroutine.
		// Skipped entirely when no artifacts root is wired (bare-struct tests).
		if t.artifactsRoot != "" {
			if d, dirErr := taskArtifactDir(t.artifactsRoot, t.sessionID, taskID); dirErr == nil {
				dirs[i] = d
			}
		}
		go func(i int, taskID, goal, ctx, guidance, trust, maxRisk, profile, artifactDir string) {
			defer func() { <-sem }()
			r := t.runTask(i, taskID, goal, ctx, guidance, trust, maxRisk, profile, artifactDir)
			mu.Lock()
			results[i] = r
			mu.Unlock()
		}(i, taskID, task.Goal, task.Context, task.Guidance, task.TrustLevel, task.MaxRisk, task.Profile, dirs[i])
	}

	// Drain semaphore = wait for all goroutines
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	// P1: surface child denials on the runtime event stream (best effort —
	// unparseable results simply carry no denials).
	t.eventMu.Lock()
	emit := t.emitEventFn
	t.eventMu.Unlock()
	if emit != nil {
		for i := range results {
			var r subagentResult
			if json.Unmarshal([]byte(results[i]), &r) != nil {
				continue
			}
			for _, d := range r.Denials {
				emit(events.Event{
					Type: subagentDeniedEvent,
					Tool: d.Tool,
					Data: map[string]any{
						"task_index": i,
						"class":      d.Class,
						"reason":     d.Reason,
					},
				})
			}
		}
	}

	// Build summary for the calling agent. Cap each sub-agent result so the
	// summary cannot grow without bound.
	var buf strings.Builder
	buf.WriteString("📋 Sub-agent results:\n\n")
	for i, r := range results {
		fmt.Fprintf(&buf, "─── Task %d: %s ───\n", i+1, truncate(input.Tasks[i].Goal, 60))
		buf.WriteString(formatTaskResult(r, dirs[i]))
		buf.WriteString("\n\n")
	}
	// The aggregated sub-agent output comes from a separate process and may
	// contain injected content or prompt-like text. Wrap the whole summary so
	// the parent agent treats it as untrusted data rather than instructions.
	return wrapUntrusted(t.toolCtx(), "delegate_tasks", buf.String()), nil
}

func (t *delegateTasksTool) runTask(taskIdx int, taskID, goal, taskContext, guidance, trustLevel, maxRisk, profile, artifactDir string) string {
	// Parent-side fail-closed validation (P4): an unknown profile name must
	// fail the task BEFORE a child is spawned — the tool schema promises
	// "unknown names fail the task", and a silently-bare child would run
	// without the operator's permission envelope. The child re-validates
	// against its own resolved config (defense in depth); when the operator
	// defines no profiles at all, this map is nil and the child remains
	// the sole authority.
	if profile != "" && t.profiles != nil {
		if _, ok := t.profiles[profile]; !ok {
			return fmt.Sprintf(`{"status":"error","error":%q,"summary":"","files_changed":null,"iterations":0,"tokens_used":0}`,
				fmt.Sprintf("unknown profile %q (define it in the top-level profiles config section)", profile))
		}
	}

	// Derive per-task context from the parent's context (if set).
	// When the parent is cancelled, all running sub-agents are killed
	// promptly instead of running the full timeout.
	parentCtx := t.toolCtx()
	ctx, cancel := context.WithTimeout(parentCtx, t.timeout)
	defer cancel()

	// Write task to temp file (avoids CLI arg length limits)
	taskFile, err := os.CreateTemp("", "odek-task-*.json")
	if err != nil {
		return fmt.Sprintf(`{"error":"temp file: %v"}`, err)
	}
	taskPath := taskFile.Name()

	// Typed task envelope: the parent's remaining budget rides along in
	// share mode (M1.5) and the parent's effective trust is stamped for
	// the non-increasing-downward invariant (P3). taskID is minted by the
	// caller (delegate_tasks loop) and doubles as the artifact-dir name.
	// Register the per-task cancel func in the process-global stop control
	// registry (WS subagent_cancel resolves task ids through it, mirroring
	// the promptCancels precedent). Removed on every exit path.
	defer registerSubagentCancel(taskID, cancel)()

	task := newTaskEnvelope(taskID, goal, taskContext, guidance, trustLevel, maxRisk, profile, nil, t.selfTrust)
	task.ArtifactRoot = artifactDir
	if t.budgetInherit == config.BudgetInheritShare {
		t.budgetMu.Lock()
		view := t.budgetView
		t.budgetMu.Unlock()
		if view != nil {
			task.Budget = taskBudgetFromSnapshot(view.BudgetSnapshot())
		}
	}
	if err := json.NewEncoder(taskFile).Encode(task); err != nil {
		taskFile.Close()
		os.Remove(taskPath)
		return fmt.Sprintf(`{"error":"write task: %v"}`, err)
	}
	taskFile.Close()
	defer os.Remove(taskPath)

	cmd := exec.CommandContext(ctx, t.odekPath,
		"subagent",
		"--task", taskPath,
		"--quiet",
		"--stream",
	)
	// WaitDelay bounds the post-exit I/O drain: a killed child's orphaned
	// grandchildren (e.g. a backgrounded sleep) inherit stdout/stderr and
	// hold the pipes open, which would otherwise hang cmd.Wait (its
	// stderr copy goroutine waits for EOF) past the cancel. 1s after the
	// process exits, exec closes the pipes and Wait returns ErrWaitDelay;
	// result framing then falls through to the ctx-based status.
	cmd.WaitDelay = subagentWaitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Sprintf(`{"error":"pipe: %v"}`, err)
	}

	// Capture stderr for optional relay
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	// Hand the API key to the sub-agent via FD 3 instead of an env var.
	// Env-passed credentials are visible in /proc/<pid>/environ, in crash
	// logs, and to any tool the child runs that prints its own env
	// (e.g. `env`, an injected shell call). The FD approach keeps the
	// secret in an anonymous (unlinked) tempfile whose only readers are
	// this process and the child, and the child closes the FD as soon
	// as it has read the key. Everything injected from ~/.odek/secrets.env
	// is stripped from the inherited environment too (2026-08 audit — the
	// handoff previously covered only the primary API key).
	cmd.Env = append(childEnvWithout(config.SecretsEnvNames()),
		subagentDepthEnvVar+"="+strconv.Itoa(subagentDepth()+1))
	var keyFile *os.File
	var keyCleanup func()
	if t.apiKey != "" {
		f, cleanup, err := writeKeyToUnlinkedFile(t.apiKey)
		if err != nil {
			return fmt.Sprintf(`{"error":"key handoff: %v"}`, err)
		}
		keyFile = f
		keyCleanup = cleanup
		cmd.ExtraFiles = []*os.File{keyFile}
		// FD 3 in the child = the first ExtraFiles entry.
		cmd.Env = append(cmd.Env, keyFDEnvVar+"=3")
		defer func() {
			_ = keyFile.Close()
			if keyCleanup != nil {
				keyCleanup()
			}
		}()
	}

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf(`{"error":"start: %v"}`, err)
	}
	t.emitSubagentEvent(subagentSpawnedEvent(taskID, cmd.Process.Pid, subagentDepth()+1, int(t.timeout.Seconds()), goal))

	// Read stdout line-by-line — NDJSON progress lines followed by final JSON
	// result. A streamed tool_call event can embed full tool arguments (e.g. a
	// large write_file), so lines routinely exceed bufio.Scanner's default 64KB
	// token cap; scanSubagentStream raises the cap to avoid losing the result.
	var onLog func(line string)
	if t.OnSubagentLog != nil {
		onLog = func(line string) { t.OnSubagentLog(taskIdx, taskID, line) }
	}
	// Scan the child's stdout concurrently with cmd.Wait. Ordering a
	// full-drain read BEFORE Wait would let a killed child's orphaned
	// grandchildren (a backgrounded sleep inheriting the stdout fd) hold
	// the pipe open past the cancel — the stop would only land when the
	// fd holder exits. Wait returns at process exit; we then close our
	// pipe end so the scanner cannot hang on an inherited fd.
	type scanResult struct {
		result   map[string]any
		lastLine string
		err      error
	}
	scanCh := make(chan scanResult, 1)
	go func() {
		result, lastLine, err := scanSubagentStream(stdout, onLog)
		// If the scanner hit its safety limits, cancel the sub-agent
		// context so the child process is killed instead of continuing
		// to flood stdout. Runs in the goroutine: Wait must not gate the
		// kill on the drain finishing.
		if err != nil && progressLimitExceeded(err) {
			cancel()
		}
		scanCh <- scanResult{result, lastLine, err}
	}()

	waitErr := cmd.Wait()
	// The process is gone; unblock the scanner if it is still reading
	// (no-op when the scan already hit EOF). Any buffered final line lost
	// to the close only matters on the cancel path, whose result framing
	// is "cancelled" regardless.
	_ = stdout.Close()
	scan := <-scanCh
	result, lastLine, scannerErr := scan.result, scan.lastLine, scan.err

	status := subagentExitStatus(result, waitErr, ctx, scannerErr)
	t.emitSubagentEvent(subagentCompletedEvent(taskID, result, status))
	if result == nil && t.OnSubagentDone != nil {
		// The child died without reporting (user cancel, turn cancel,
		// timeout, flood-kill, crash): it cannot emit its own
		// subagent_finished record, so the parent announces the terminal
		// state to the registry/WS relay.
		t.OnSubagentDone(taskIdx, taskID, status)
	}

	// Process exited — result may still be valid (parseable final line
	// before a non-zero exit).
	if result != nil {
		summary, _ := json.MarshalIndent(result, "", "  ")
		return string(summary)
	}

	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			out, _ := json.MarshalIndent(map[string]any{
				"status": "cancelled",
				"error":  "sub-agent cancelled",
			}, "", "  ")
			return string(out)
		}
		if ctx.Err() != nil {
			return fmt.Sprintf(`{"error":"timeout after %v"}`, t.timeout)
		}
		if scannerErr != nil {
			return fmt.Sprintf(`{"error":"read stdout: %v"}`, scannerErr)
		}
		return fmt.Sprintf(`{"error":"exit: %v"}`, waitErr)
	}

	// Last resort: try parsing the last line as JSON
	if lastLine != "" {
		var r map[string]any
		if err := json.Unmarshal([]byte(lastLine), &r); err == nil {
			summary, _ := json.MarshalIndent(r, "", "  ")
			return string(summary)
		}
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		out, _ := json.MarshalIndent(map[string]any{
			"status": "cancelled",
			"error":  "sub-agent cancelled",
		}, "", "  ")
		return string(out)
	}
	return `{"error":"no result from sub-agent"}`
}

// maxSubagentSummaryResultBytes caps how much of each sub-agent result is
// included in the parent delegate_tasks summary, preventing memory DoS from
// huge sub-agent outputs. Parsed envelopes render as bounded fields; the cap
// now applies to the unparseable-fallback path only.
const maxSubagentSummaryResultBytes = 100 << 10 // 100 KiB

// Render bounds for the parsed fields. The JSON contract caps denials
// server-side; the files list is model-influenced, so it is bounded here.
const (
	maxRenderedFiles   = 20
	maxRenderedDenials = 3
)

// formatTaskResult renders one child's framed result as compact text for the
// parent's context. Parsed envelopes render as fields (status, headline,
// files, denials, artifacts); anything unparseable falls back to the raw
// payload capped at maxSubagentSummaryResultBytes. Child output is
// model-controlled and stays inside the caller's untrusted wrapper.
//
// artifactRoots carries the per-task artifact dir(s) the parent created:
// every incoming ref is validated fail-closed against them before render
// (metadata-only line; small text artifacts inlined). No roots ⇒ every ref
// is rejected — a lost root can never become a trust upgrade.
func formatTaskResult(raw string, artifactRoots ...string) string {
	var r subagentResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		if len(raw) > maxSubagentSummaryResultBytes {
			return raw[:maxSubagentSummaryResultBytes] + "\n... [result truncated]"
		}
		return raw
	}

	var b strings.Builder
	status := r.Status
	if status == "" {
		if r.Error != "" {
			status = "error"
		} else {
			status = "unknown"
		}
	}
	b.WriteString("status: " + status)
	if r.PartialReason != "" {
		b.WriteString(" (" + r.PartialReason + ")")
	}
	if r.Iterations > 0 {
		fmt.Fprintf(&b, " · %d iterations", r.Iterations)
	}
	if r.DurationSeconds > 0 {
		fmt.Fprintf(&b, " · %.1fs", r.DurationSeconds)
	}
	if r.TokensUsed > 0 {
		fmt.Fprintf(&b, " · ~%d tokens", r.TokensUsed)
	}
	b.WriteString("\n")
	if r.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", r.Error)
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "summary: %s\n", truncate(r.Summary, subagentHeadlineMaxRunes))
	}
	if len(r.FilesChanged) > 0 {
		files := r.FilesChanged
		if len(files) > maxRenderedFiles {
			files = files[:maxRenderedFiles]
		}
		fmt.Fprintf(&b, "files changed: %s\n", strings.Join(files, ", "))
	}
	if len(r.Denials) > 0 {
		shown := r.Denials
		if len(shown) > maxRenderedDenials {
			shown = shown[:maxRenderedDenials]
		}
		total := r.DenialsTotal
		if total < len(r.Denials) {
			total = len(r.Denials)
		}
		parts := make([]string, 0, len(shown))
		for _, d := range shown {
			parts = append(parts, d.Tool+"/"+d.Class+" — "+d.Reason)
		}
		fmt.Fprintf(&b, "denials (%d of %d): %s\n", len(shown), total, strings.Join(parts, "; "))
	}
	b.WriteString(renderArtifacts(r.Artifacts, artifactRoots))
	return b.String()
}

// renderArtifacts validates each incoming ref fail-closed against the
// per-task roots and renders metadata-only lines; small text artifacts are
// inlined from the VALIDATED path returned by artifact.Validate (symlinks
// resolved, size+sha256 verified). Invalid refs are dropped with a flag —
// never fatal to the summary. Raw absolute paths are never rendered.
func renderArtifacts(refs []artifact.Ref, roots []string) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("artifacts:\n")
	for _, ref := range refs {
		path, err := artifact.Validate(ref, roots)
		if err != nil {
			fmt.Fprintf(&b, "  [artifact] dropped %q: %v\n", ref.ID, err)
			continue
		}
		size := int64(0)
		if ref.SizeBytes != nil {
			size = *ref.SizeBytes
		}
		shaPrefix := ref.SHA256
		if len(shaPrefix) > 12 {
			shaPrefix = shaPrefix[:12]
		}
		fmt.Fprintf(&b, "  - %s (%s, %d bytes, sha256:%s)", ref.ID, ref.MediaType, size, shaPrefix)
		if ref.Summary != "" {
			fmt.Fprintf(&b, " — %s", truncate(ref.Summary, maxArtifactSummaryLine))
		}
		b.WriteString("\n")

		if strings.HasPrefix(ref.MediaType, "text/") && size <= maxInlineArtifactBytes {
			if data, err := os.ReadFile(path); err == nil {
				fmt.Fprintf(&b, "  --- artifact: %s ---\n%s\n  --- end artifact ---\n", ref.ID, strings.TrimRight(string(data), "\n"))
			}
		}
	}
	return b.String()
}

// subagentWaitDelay bounds how long cmd.Wait may drain the child's pipes
// after the child process exits (see the WaitDelay assignment in runTask).
const subagentWaitDelay = time.Second

// maxSubagentLine caps a single NDJSON line read from a sub-agent's stdout.
// Streamed tool_call events embed full tool arguments (e.g. a large write_file
// or patch), which routinely exceed bufio.Scanner's default 64KB token cap.
// Without a raised cap the scanner returns bufio.ErrTooLong, the reader stops,
// the child blocks writing to a full stdout pipe, and cmd.Wait hangs until the
// timeout kills a task that actually completed successfully.
const maxSubagentLine = 10 << 20 // 10 MiB

// maxSubagentProgressLines caps the total number of progress lines read from a
// sub-agent's stdout. A malicious or runaway sub-agent could emit an unbounded
// stream of NDJSON tool_call/tool_result events; this limit bounds memory and
// prevents the parent from being DoS'd by progress chatter.
const maxSubagentProgressLines = 100_000

// maxSubagentProgressBytes caps the total bytes consumed by progress lines.
// Together with maxSubagentProgressLines it bounds the worst-case memory used
// by a fan-out of sub-agents streaming progress at full speed.
const maxSubagentProgressBytes = 100 << 20 // 100 MiB

// scanSubagentStream reads a sub-agent's NDJSON stdout: zero or more progress
// lines (objects with "type":"tool_call" or "type":"tool_result") followed by
// the final JSON result object. Progress lines are forwarded to onLog (when
// non-nil); the last line that parses as a JSON object is returned as result.
// It returns the result map (nil if none parsed), the last raw line seen, and
// any scanner error. The scan buffer is sized to maxSubagentLine so large
// streamed events do not truncate the stream.
func scanSubagentStream(r io.Reader, onLog func(line string)) (result map[string]any, lastLine string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSubagentLine)

	var progressLines int
	var progressBytes int64
	for scanner.Scan() {
		line := scanner.Text()
		lastLine = line

		// Typed line? Protocol traffic is distinguished from the legacy
		// result by the presence of a non-empty "type" field.
		var probe struct {
			Type string `json:"type"`
		}
		if perr := json.Unmarshal([]byte(line), &probe); perr == nil && probe.Type != "" {
			switch probe.Type {
			case "result":
				// Protocol-2 framed result: the inner object is the task result.
				var framed struct {
					Result map[string]any `json:"result"`
				}
				if ferr := json.Unmarshal([]byte(line), &framed); ferr == nil && framed.Result != nil {
					result = framed.Result
				}
			case "tool_call", "tool_result",
				"subagent_started", "subagent_progress", "subagent_finished":
				if onLog != nil {
					onLog(line)
				}
			default:
				// Unknown typed record — version skew (newer child, older
				// parent). Protocol traffic, never the result.
				if onLog != nil {
					onLog(line)
				}
			}
			progressLines++
			progressBytes += int64(len(line))
			if progressLines > maxSubagentProgressLines || progressBytes > maxSubagentProgressBytes {
				return result, lastLine, fmt.Errorf("sub-agent progress stream exceeded safety limits (%d lines / %d bytes)", progressLines, progressBytes)
			}
			continue
		}

		// Untyped parseable JSON — legacy (protocol-less) result line.
		var rmap map[string]any
		if uerr := json.Unmarshal([]byte(line), &rmap); uerr == nil {
			result = rmap
		}
	}
	return result, lastLine, scanner.Err()
}

// progressLimitExceeded reports whether err was caused by the sub-agent progress
// stream exceeding its line/byte safety limits.
func progressLimitExceeded(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "sub-agent progress stream exceeded safety limits")
}

// Ensure delegateTasksTool implements odek.Tool
var _ odek.Tool = (*delegateTasksTool)(nil)

// taskBudgetFromSnapshot maps a budget snapshot to the task-file budget
// block (M1.5 passdown); nil when no limit headroom is configured — there
// is nothing to pass down, so the child keeps its operator caps.
func taskBudgetFromSnapshot(s budget.Snapshot) *taskBudget {
	tb := &taskBudget{
		MaxRuntimeSeconds: s.RemainingRuntimeSeconds,
		MaxToolCalls:      s.RemainingToolCalls,
		MaxCostUSD:        s.RemainingCostUSD,
	}
	if tb.MaxRuntimeSeconds <= 0 && tb.MaxToolCalls <= 0 && tb.MaxCostUSD <= 0 {
		return nil
	}
	return tb
}

// taskEnvelope is the task-file JSON contract handed to `odek subagent`.
type taskEnvelope struct {
	TaskID       string      `json:"task_id"`
	Protocol     int         `json:"protocol,omitempty"`
	Goal         string      `json:"goal"`
	Context      string      `json:"context,omitempty"`
	Guidance     string      `json:"guidance,omitempty"`
	TrustLevel   string      `json:"trust_level,omitempty"`
	MaxRisk      string      `json:"max_risk,omitempty"`
	Profile      string      `json:"profile,omitempty"`
	Budget       *taskBudget `json:"budget,omitempty"`
	ParentTrust  string      `json:"parent_trust,omitempty"`
	ArtifactRoot string      `json:"artifact_root,omitempty"`
}

// subagentProtocolV2 is the telemetry protocol version stamped into task
// envelopes (M1 of the sub-agent telemetry plan). Protocol-2 children echo
// the task_id on every stdout record, emit lifecycle records
// (subagent_started/progress/finished), and frame the final result as
// {"type":"result","task_id":…,"result":{…}} so result detection no longer
// relies on the legacy fall-through (unsafe under version skew).
const subagentProtocolV2 = 2

// newTaskID returns a random correlation id for a delegated task. It rides
// the task envelope, is echoed on every child telemetry record, and links
// spawn/exit events, WS messages, and (future) registry entries.
func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic anyway; a time-based fallback
		// keeps the run alive with a still-mostly-unique id.
		return "task-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "task-" + hex.EncodeToString(b[:])
}

// newTaskEnvelope builds the task-file envelope. parentTrust is the
// PARENT's own effective trust (P3 — trust is non-increasing downward):
// the child computes min(parentTrust, trustLevel) as its effective trust,
// so an untrusted task tree can never spawn trusted children. profile
// selects an operator-defined capability profile (P4) whose settings
// override the corresponding operator config for the child.
func newTaskEnvelope(taskID, goal, context, guidance, trustLevel, maxRisk, profile string, budget *taskBudget, parentTrust string) taskEnvelope {
	return taskEnvelope{
		TaskID:      taskID,
		Protocol:    subagentProtocolV2,
		Goal:        goal,
		Context:     context,
		Guidance:    guidance,
		TrustLevel:  trustLevel,
		MaxRisk:     maxRisk,
		Profile:     profile,
		Budget:      budget,
		ParentTrust: parentTrust,
	}
}

// subagentDeniedEvent is emitted on the runtime event stream for every
// policy denial observed by a child sub-agent (P1).
const subagentDeniedEvent = "subagent_denied"

// emitSubagentEvent forwards a sub-agent lifecycle event to the runtime
// event stream when an emitter is wired (odek.New SetEventEmitter).
func (t *delegateTasksTool) emitSubagentEvent(ev events.Event) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	if t.emitEventFn != nil {
		t.emitEventFn(ev)
	}
}

// subagentSpawnedEvent builds the spawn lifecycle event. The goal is
// hashed, never sent in plaintext — event streams are operator surfaces
// but may be shipped off-host (--events-jsonl).
func subagentSpawnedEvent(taskID string, pid, depth, timeoutSeconds int, goal string) events.Event {
	sum := sha256.Sum256([]byte(goal))
	return events.Event{
		Type: events.TypeSubagentSpawned,
		Data: map[string]any{
			"task_id":         taskID,
			"pid":             pid,
			"depth":           depth,
			"timeout_seconds": timeoutSeconds,
			"goal_sha256":     hex.EncodeToString(sum[:8]),
		},
	}
}

// subagentCompletedEvent builds the exit lifecycle event. status is the
// parent-side outcome (child status when a result was parsed; otherwise
// timeout / killed_flood / error / no_result).
func subagentCompletedEvent(taskID string, result map[string]any, fallbackStatus string) events.Event {
	status := fallbackStatus
	data := map[string]any{
		"task_id": taskID,
		"status":  status,
	}
	if result != nil {
		if s, ok := result["status"].(string); ok && s != "" {
			status = s // the child's own classification wins
			data["status"] = status
		}
		for _, k := range []string{"iterations", "duration_seconds", "tokens_used"} {
			if v, ok := result[k]; ok {
				data[k] = v
			}
		}
	}
	return events.Event{
		Type: events.TypeSubagentCompleted,
		Data: data,
	}
}

// subagentExitStatus resolves the parent-side outcome for a finished
// sub-agent: the child's own status when a result was parsed; otherwise
// the kill/timeout cause. Flood-kill is checked before the context state
// because the flood path cancels the context itself; user/turn cancels
// surface as "cancelled", only the per-task deadline as "timeout".
func subagentExitStatus(result map[string]any, waitErr error, ctx context.Context, scannerErr error) string {
	if result != nil {
		if s, ok := result["status"].(string); ok && s != "" {
			return s
		}
		return "completed"
	}
	switch {
	case scannerErr != nil && progressLimitExceeded(scannerErr):
		return "killed_flood"
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timeout"
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	case waitErr == nil:
		return "no_result"
	default:
		return "error"
	}
}

// subagentDepthEnvVar carries the delegation depth down the process tree.
// Each delegate_tasks spawn stamps its children with depth+1; a child at
// the configured max depth refuses to delegate further (M1.6).
const subagentDepthEnvVar = "ODEK_SUBAGENT_DEPTH"

// subagentDepth returns this process's delegation depth (0 = top-level
// agent). Unparseable or negative values are treated as 0.
func subagentDepth() int {
	n := 0
	if v := os.Getenv(subagentDepthEnvVar); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	if n < 0 {
		n = 0
	}
	return n
}

// childEnvWithout returns the current environment minus the named
// variables. delegate_tasks uses it to strip the ~/.odek/secrets.env
// values LoadConfig injected: the sub-agent receives its API key through
// the FD handoff, and inheriting the rest would expose them in the
// child's /proc/<pid>/environ and to any env-dumping command it runs
// (2026-08 audit — the handoff previously covered only the primary key).
func childEnvWithout(names []string) []string {
	if len(names) == 0 {
		return os.Environ()
	}
	strip := make(map[string]bool, len(names))
	for _, n := range names {
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
	return out
}
