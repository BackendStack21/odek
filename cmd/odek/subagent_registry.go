package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/redact"
)

// ── Sub-agent registry (telemetry M2) ────────────────────────────────
//
// A serve-global, bounded ring of sub-agent lifecycle entries. The Web UI's
// cards are driven by live WS subagent_state messages; this registry is the
// reload/replay half — GET /api/subagents returns a snapshot so a page
// reload mid-batch restores state. Entries are operator-surface data
// (session-scoped auth applies); goals are redacted + truncated.

const (
	// maxSubagentRegistryEntries bounds the ring; oldest entries are evicted.
	maxSubagentRegistryEntries = 256
	// maxSubagentRegistryGoalChars truncates stored goals. Raised from 200
	// to 2048 (wire v2): the goal rides every subagent_state frame so
	// clients (e.g. bodek) can render usable task text, not an ellipsis.
	maxSubagentRegistryGoalChars = 2048
)

// subagentArtifact is the bounded metadata block the registry and the
// subagent_state terminal frame carry for one artifact produced by a
// sub-agent (wire v2). Metadata only — content is never inlined; the
// model-facing render path keeps the fail-closed artifact.Validate gate.
type subagentArtifact struct {
	ID    string `json:"id"`
	Path  string `json:"path,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
}

// subagentEntry is one delegated task's lifecycle record.
type subagentEntry struct {
	TaskID          string             `json:"task_id"`
	RunKey          string             `json:"run_key"`
	Goal            string             `json:"goal,omitempty"`
	Status          string             `json:"status,omitempty"`
	Phase           string             `json:"phase"` // queued | started | active | finished
	PID             int                `json:"pid,omitempty"`
	StartedAt       time.Time          `json:"started_at"`
	FinishedAt      time.Time          `json:"finished_at,omitempty"`
	Iterations      int                `json:"iterations,omitempty"`
	Step            int                `json:"step,omitempty"`
	LastTool        string             `json:"last_tool,omitempty"`
	DurationSeconds float64            `json:"duration_seconds,omitempty"`
	TokensUsed      int                `json:"tokens_used,omitempty"`
	Profile         string             `json:"profile,omitempty"`    // requested on queued; effective (post-clamp) once the child reports
	MaxRisk         string             `json:"max_risk,omitempty"`   // requested on queued; effective (post-clamp) once the child reports
	BudgetSeconds   int                `json:"budget_seconds,omitempty"`
	BudgetIterations int               `json:"budget_iterations,omitempty"`
	CostUSD         float64            `json:"cost_usd,omitempty"`         // cumulative child-reported spend
	BudgetCostUSD   float64            `json:"budget_cost_usd,omitempty"`  // present only when the child reports a cost cap
	Artifacts       []subagentArtifact `json:"artifacts,omitempty"`        // terminal metadata from the framed result envelope
}

var subagentReg = struct {
	mu      sync.Mutex
	entries []*subagentEntry // oldest first
	byID    map[string]*subagentEntry
}{
	byID: map[string]*subagentEntry{},
}

// subagentRegistryRecord inserts a new entry (or merges into the existing
// one by task_id) and evicts the oldest when the ring overflows.
func subagentRegistryRecord(e *subagentEntry) {
	subagentReg.mu.Lock()
	defer subagentReg.mu.Unlock()
	e.TaskID = strings.TrimSpace(e.TaskID)
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	if prev, ok := subagentReg.byID[e.TaskID]; ok {
		// Merge: the new record's set fields win, zero/empty fields keep
		// the accumulated state. The v1 restore-old-into-new behavior made
		// any re-record a no-op — the wire-v2 queued → started transition
		// relies on the started record overwriting the declared identity
		// (profile/max_risk) with the effective values and carrying the
		// budgets in.
		mergeEntry(prev, e)
		return
	}
	cp := *e
	subagentReg.entries = append(subagentReg.entries, &cp)
	subagentReg.byID[e.TaskID] = &cp
	if len(subagentReg.entries) > maxSubagentRegistryEntries {
		// Prefer evicting the oldest FINISHED entry: ring pressure must not
		// erase a still-running task's card (its next progress record would
		// recreate a hollow entry without RunKey, making it invisible to
		// run-filtered snapshots — the reload-restore path this registry
		// exists for). Fall back to the oldest entry only when every entry
		// is live (unreachable in practice: concurrency ≪ ring size).
		evictIdx := 0
		for i, e := range subagentReg.entries {
			if e.Phase == "finished" {
				evictIdx = i
				break
			}
		}
		oldest := subagentReg.entries[evictIdx]
		subagentReg.entries = append(subagentReg.entries[:evictIdx], subagentReg.entries[evictIdx+1:]...)
		delete(subagentReg.byID, oldest.TaskID)
	}
}

// mergeEntry folds a fresh record into the accumulated entry: every set
// field on src wins; zero/empty fields keep dst's accumulated value.
func mergeEntry(dst, src *subagentEntry) {
	if src.Goal != "" {
		dst.Goal = src.Goal
	}
	if src.RunKey != "" {
		dst.RunKey = src.RunKey
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.Phase != "" {
		dst.Phase = src.Phase
	}
	if src.PID != 0 {
		dst.PID = src.PID
	}
	if !src.StartedAt.IsZero() {
		dst.StartedAt = src.StartedAt
	}
	if !src.FinishedAt.IsZero() {
		dst.FinishedAt = src.FinishedAt
	}
	if src.Iterations != 0 {
		dst.Iterations = src.Iterations
	}
	if src.Step != 0 {
		dst.Step = src.Step
	}
	if src.LastTool != "" {
		dst.LastTool = src.LastTool
	}
	if src.DurationSeconds != 0 {
		dst.DurationSeconds = src.DurationSeconds
	}
	if src.TokensUsed != 0 {
		dst.TokensUsed = src.TokensUsed
	}
	if src.Profile != "" {
		dst.Profile = src.Profile
	}
	if src.MaxRisk != "" {
		dst.MaxRisk = src.MaxRisk
	}
	if src.BudgetSeconds != 0 {
		dst.BudgetSeconds = src.BudgetSeconds
	}
	if src.BudgetIterations != 0 {
		dst.BudgetIterations = src.BudgetIterations
	}
	if src.CostUSD != 0 {
		dst.CostUSD = src.CostUSD
	}
	if src.BudgetCostUSD != 0 {
		dst.BudgetCostUSD = src.BudgetCostUSD
	}
	if len(src.Artifacts) > 0 {
		dst.Artifacts = src.Artifacts
	}
}

// subagentRegistryUpdate applies fn to the entry with the given task_id
// (no-op when absent). New entries are auto-created so a progress line that
// races its started record still lands; runKey stamps such recreated entries
// (eviction race / out-of-order arrival) so run-filtered snapshots keep
// seeing the task.
func subagentRegistryUpdate(taskID, runKey string, fn func(*subagentEntry)) {
	subagentRegistryRecord(&subagentEntry{TaskID: taskID, RunKey: runKey})
	subagentReg.mu.Lock()
	defer subagentReg.mu.Unlock()
	if e, ok := subagentReg.byID[taskID]; ok {
		fn(e)
	}
}

// subagentRegistrySnapshot returns a copy of the entries, optionally
// filtered by run key (empty = all), oldest first.
func subagentRegistrySnapshot(runKey string) []subagentEntry {
	subagentReg.mu.Lock()
	defer subagentReg.mu.Unlock()
	out := make([]subagentEntry, 0, len(subagentReg.entries))
	for _, e := range subagentReg.entries {
		if runKey == "" || e.RunKey == runKey {
			out = append(out, *e)
		}
	}
	return out
}

// ── Telemetry relay: log fan-out + state fan-out + registry ──────────

// newSubagentTelemetryRelay wraps the M1 log relay: every line is relayed
// as a (redacted, capped) subagent_log message; lifecycle records
// additionally update the registry and emit a subagent_state WS message.
func newSubagentTelemetryRelay(send func(v any) error, runKey string) func(taskIdx int, taskID string, line string) {
	logRelay := newSubagentLogRelay(send)
	return func(taskIdx int, taskID string, line string) {
		logRelay(taskIdx, taskID, line)

		var rec struct {
			Type             string             `json:"type"`
			TaskID           string             `json:"task_id,omitempty"`
			PID              int                `json:"pid,omitempty"`
			Goal             string             `json:"goal,omitempty"`
			Status           string             `json:"status,omitempty"`
			Step             int                `json:"step,omitempty"`
			Tool             string             `json:"tool,omitempty"`
			Iterations       int                `json:"iterations,omitempty"`
			DurationS        float64            `json:"duration_s,omitempty"`
			TokensUsed       int                `json:"tokens_used,omitempty"`
			Profile          string             `json:"profile,omitempty"`
			MaxRisk          string             `json:"max_risk,omitempty"`
			BudgetSeconds    int                `json:"budget_seconds,omitempty"`
			BudgetIterations int                `json:"budget_iterations,omitempty"`
			CostUSD          float64            `json:"cost_usd,omitempty"`
			BudgetCostUSD    float64            `json:"budget_cost_usd,omitempty"`
			Artifacts        []subagentArtifact `json:"artifacts,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return
		}
		if rec.TaskID != "" {
			taskID = rec.TaskID
		}
		if taskID == "" {
			return // no correlation id — nothing to track
		}

		switch rec.Type {
		case "subagent_queued":
			// Parent-synthesized (delegate_tasks pre-spawn): the task was
			// accepted but has not been spawned — the concurrency limiter
			// may still be holding it. profile/max_risk carry the DECLARED
			// values; the child's started record overwrites them with the
			// effective post-clamp values.
			subagentRegistryRecord(&subagentEntry{
				TaskID:    taskID,
				RunKey:    runKey,
				Goal:      redactGoal(rec.Goal),
				Profile:   rec.Profile,
				MaxRisk:   rec.MaxRisk,
				Phase:     "queued",
				Status:    "queued",
				StartedAt: time.Now(),
			})
		case "subagent_started":
			subagentRegistryRecord(&subagentEntry{
				TaskID:           taskID,
				RunKey:           runKey,
				Goal:             redactGoal(rec.Goal),
				Phase:            "started",
				Status:           "running",
				PID:              rec.PID,
				StartedAt:        time.Now(),
				Profile:          rec.Profile,
				MaxRisk:          rec.MaxRisk,
				BudgetSeconds:    rec.BudgetSeconds,
				BudgetIterations: rec.BudgetIterations,
				CostUSD:          rec.CostUSD,
				BudgetCostUSD:    rec.BudgetCostUSD,
			})
		case "subagent_progress":
			subagentRegistryUpdate(taskID, runKey, func(e *subagentEntry) {
				e.Phase = "active"
				e.Step = rec.Step
				e.LastTool = rec.Tool
				// Budget/identity fields are re-asserted (and may first
				// arrive) on progress records; last report wins.
				if rec.Profile != "" {
					e.Profile = rec.Profile
				}
				if rec.MaxRisk != "" {
					e.MaxRisk = rec.MaxRisk
				}
				if rec.BudgetSeconds > 0 {
					e.BudgetSeconds = rec.BudgetSeconds
				}
				if rec.BudgetIterations > 0 {
					e.BudgetIterations = rec.BudgetIterations
				}
				if rec.CostUSD > 0 {
					e.CostUSD = rec.CostUSD
				}
				if rec.BudgetCostUSD > 0 {
					e.BudgetCostUSD = rec.BudgetCostUSD
				}
			})
		case "subagent_finished":
			subagentRegistryUpdate(taskID, runKey, func(e *subagentEntry) {
				e.Phase = "finished"
				e.Status = rec.Status
				e.Iterations = rec.Iterations
				e.DurationSeconds = rec.DurationS
				e.TokensUsed = rec.TokensUsed
				if rec.CostUSD > 0 {
					e.CostUSD = rec.CostUSD
				}
				if rec.BudgetCostUSD > 0 {
					e.BudgetCostUSD = rec.BudgetCostUSD
				}
				if len(rec.Artifacts) > 0 {
					e.Artifacts = rec.Artifacts
				}
				e.FinishedAt = time.Now()
			})
			if rec.Status == "success" || rec.Status == "partial" {
				subagentStats.completed.Add(1)
			} else {
				subagentStats.failed.Add(1)
			}
			subagentStats.tokens.Add(int64(rec.TokensUsed))
			if rec.CostUSD > 0 {
				subagentStats.costMicros.Add(int64(rec.CostUSD * 1e6))
			}
		case "subagent_result":
			// Parent-synthesized refinement: cost + artifact metadata
			// extracted from the framed result envelope (the envelope
			// itself never reaches this relay). Merge-only — phase,
			// status, and lifetime counters stay owned by the
			// subagent_finished / done-relay paths.
			subagentRegistryUpdate(taskID, runKey, func(e *subagentEntry) {
				if rec.CostUSD > 0 {
					e.CostUSD = rec.CostUSD
				}
				if rec.BudgetCostUSD > 0 {
					e.BudgetCostUSD = rec.BudgetCostUSD
				}
				if len(rec.Artifacts) > 0 {
					e.Artifacts = rec.Artifacts
				}
			})
		default:
			return // tool_call/tool_result/unknown — log-only
		}

		// Fan the state transition out to the UI.
		subagentRegistryEmitState(send, taskID, taskIdx)
	}
}

// redactGoal redacts and caps a goal for storage/relay (operator surface,
// but model-controlled text — same treatment as the log relay).
func redactGoal(goal string) string {
	goal = redact.RedactSecrets(goal)
	// Rune-safe truncation: the constant promises chars, and a byte slice
	// at the boundary split multi-byte runes, corrupting the goal text with
	// invalid UTF-8 exactly when the clamp engaged (long goals are the
	// normal case for real tasks).
	if r := []rune(goal); len(r) > maxSubagentRegistryGoalChars {
		goal = string(r[:maxSubagentRegistryGoalChars])
	}
	return goal
}

// subagentRegistryEmitState fans a task's current registry entry out to
// the UI as a subagent_state WS message. Shared by the line relay
// (child-driven transitions) and the done relay (parent-driven terminal
// states for children that died without reporting).
func subagentRegistryEmitState(send func(v any) error, taskID string, taskIdx int) {
	snap := subagentRegistrySnapshot("")
	for _, e := range snap {
		if e.TaskID == taskID {
			msg := map[string]any{
				"type":             "subagent_state",
				"task_id":          e.TaskID,
				"task_idx":         taskIdx,
				"run_key":          e.RunKey,
				"phase":            e.Phase,
				"status":           e.Status,
				"step":             e.Step,
				"iterations":       e.Iterations,
				"tool":             e.LastTool,
				"duration_seconds": e.DurationSeconds,
				"tokens_used":      e.TokensUsed,
			}
			// Wire v2 fields — omitted when unset (0/empty), matching the
			// registry entry's omitempty JSON contract.
			if e.Goal != "" {
				msg["goal"] = e.Goal
			}
			if e.Profile != "" {
				msg["profile"] = e.Profile
			}
			if e.MaxRisk != "" {
				msg["max_risk"] = e.MaxRisk
			}
			if e.BudgetSeconds > 0 {
				msg["budget_seconds"] = e.BudgetSeconds
			}
			if e.BudgetIterations > 0 {
				msg["budget_iterations"] = e.BudgetIterations
			}
			if e.CostUSD > 0 {
				msg["cost_usd"] = e.CostUSD
			}
			if e.BudgetCostUSD > 0 {
				msg["budget_cost_usd"] = e.BudgetCostUSD
			}
			if len(e.Artifacts) > 0 {
				msg["artifacts"] = e.Artifacts
			}
			_ = send(msg)
			break
		}
	}
}

// ── Sub-agent stop control (per-card stop) ──────────────────────────
//
// Process-global registry of live per-task cancel functions, mirroring
// the promptCancels precedent: the WS subagent_cancel handler resolves a
// task_id to its cancel func without needing the owning connection's
// tool instance. Entries exist ONLY while a task is running — runTask
// registers before spawn and unregisters on every exit path, so the map
// is bounded by live tasks and stale ids are naturally rejected.

var subagentCtl struct {
	mu   sync.Mutex
	byID map[string]context.CancelFunc
}

// registerSubagentCancel records cancel as the live cancel func for
// taskID. The returned unregister removes the entry unconditionally —
// task ids are random per-task UUIDs, so no generation guard is needed.
func registerSubagentCancel(taskID string, cancel context.CancelFunc) (unregister func()) {
	if taskID == "" || cancel == nil {
		return func() {}
	}
	subagentCtl.mu.Lock()
	if subagentCtl.byID == nil {
		subagentCtl.byID = map[string]context.CancelFunc{}
	}
	subagentCtl.byID[taskID] = cancel
	subagentCtl.mu.Unlock()

	return func() {
		subagentCtl.mu.Lock()
		delete(subagentCtl.byID, taskID)
		subagentCtl.mu.Unlock()
	}
}

// cancelSubagentTask cancels the running sub-agent for taskID, if any.
// Returns true when a live task was found and its cancel func invoked.
func cancelSubagentTask(taskID string) bool {
	if taskID == "" {
		return false
	}
	subagentCtl.mu.Lock()
	cancel, ok := subagentCtl.byID[taskID]
	subagentCtl.mu.Unlock()
	if !ok || cancel == nil {
		return false
	}
	cancel()
	return true
}

// newSubagentDoneRelay bridges parent-side terminal outcomes for
// sub-agents that died WITHOUT emitting their own subagent_finished
// record (user cancel, turn cancel, timeout, flood-kill, crash) into the
// registry and the WS subagent_state stream, so cards do not stay
// "running" forever. Known double-count edge: a child that emitted
// subagent_finished but whose framed result still failed to parse is
// counted twice in the failure counter — a counter-only inaccuracy on a
// malformed-output path.
func newSubagentDoneRelay(send func(v any) error, runKey string) func(taskIdx int, taskID, status string) {
	return func(taskIdx int, taskID, status string) {
		if taskID == "" {
			return
		}
		// Update (auto-creates) rather than Record: Record's re-record
		// path restores accumulated state, which would clobber the
		// terminal transition on an existing entry.
		// First terminal report wins: this relay exists for children that
		// died without reporting (or whose framed result never parsed). When
		// the child itself already reported a terminal status via its
		// telemetry stream, overwriting it flipped a reported success to
		// "failed" on the card AND double-counted the task in the lifetime
		// counters.
		alreadyTerminal := false
		subagentRegistryUpdate(taskID, runKey, func(e *subagentEntry) {
			if e.Phase == "finished" && (e.Status == "success" || e.Status == "partial") {
				alreadyTerminal = true
				return
			}
			e.Phase = "finished"
			e.Status = status
			e.FinishedAt = time.Now()
		})
		// A user-initiated cancel is neither success nor failure for the
		// lifetime counters; every other no-result outcome is a failure.
		if !alreadyTerminal && status != "cancelled" {
			subagentStats.failed.Add(1)
		}
		// Fan the terminal state out to the UI.
		subagentRegistryEmitState(send, taskID, taskIdx)
	}
}

// ── Lifetime sub-agent counters (telemetry M3) ──────────────────────

var subagentStats struct {
	completed atomic.Int64
	failed    atomic.Int64
	tokens    atomic.Int64
	// costMicros accumulates the child-reported final cost estimates in
	// micro-USD (int64 atomic — float atomics are not available). Exposed
	// as the subagent_cost_usd sub-total so clients can render a total
	// including sub-agents without client-side arithmetic.
	costMicros atomic.Int64
}

// subagentStatsSnapshot returns the lifetime sub-agent counters plus the
// number of currently non-finished registry entries. Queued entries
// (accepted but not yet spawned) count as neither active nor finished.
func subagentStatsSnapshot() map[string]any {
	active := 0
	for _, e := range subagentRegistrySnapshot("") {
		// queued tasks are accepted-but-not-spawned: neither live nor done.
		if e.Phase == "started" || e.Phase == "active" {
			active++
		}
	}
	return map[string]any{
		"completed":   subagentStats.completed.Load(),
		"failed":      subagentStats.failed.Load(),
		"active":      active,
		"tokens_used": subagentStats.tokens.Load(),
		"cost_usd":    float64(subagentStats.costMicros.Load()) / 1e6,
	}
}

// handleSubagentRegistry serves GET /api/subagents — the registry snapshot
// (optionally filtered by ?key=<run_key>). Auth is enforced by the apiAuth
// wrapper at mux registration, same as /api/events.
func handleSubagentRegistry() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := subagentRegistrySnapshot(r.URL.Query().Get("key"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries": entries,
			"count":   len(entries),
		})
	})
}
