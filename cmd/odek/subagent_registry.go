package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
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
	// maxSubagentRegistryGoalChars truncates stored goals.
	maxSubagentRegistryGoalChars = 200
)

// subagentEntry is one delegated task's lifecycle record.
type subagentEntry struct {
	TaskID          string    `json:"task_id"`
	RunKey          string    `json:"run_key"`
	Goal            string    `json:"goal,omitempty"`
	Status          string    `json:"status,omitempty"`
	Phase           string    `json:"phase"` // started | active | finished
	PID             int       `json:"pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Iterations      int       `json:"iterations,omitempty"`
	Step            int       `json:"step,omitempty"`
	LastTool        string    `json:"last_tool,omitempty"`
	DurationSeconds float64   `json:"duration_seconds,omitempty"`
	TokensUsed      int       `json:"tokens_used,omitempty"`
}

var subagentReg = struct {
	mu      sync.Mutex
	entries []*subagentEntry // oldest first
	byID    map[string]*subagentEntry
}{
	byID: map[string]*subagentEntry{},
}

// subagentRegistryRecord inserts a new entry (or replaces by task_id) and
// evicts the oldest when the ring overflows.
func subagentRegistryRecord(e *subagentEntry) {
	subagentReg.mu.Lock()
	defer subagentReg.mu.Unlock()
	if prev, ok := subagentReg.byID[e.TaskID]; ok {
		*e = *prev // re-record keeps accumulated state
	}
	e.TaskID = strings.TrimSpace(e.TaskID)
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	if prev, ok := subagentReg.byID[e.TaskID]; ok {
		// Replace in place, preserving ring position.
		*prev = *e
		return
	}
	cp := *e
	subagentReg.entries = append(subagentReg.entries, &cp)
	subagentReg.byID[e.TaskID] = &cp
	if len(subagentReg.entries) > maxSubagentRegistryEntries {
		oldest := subagentReg.entries[0]
		subagentReg.entries = subagentReg.entries[1:]
		delete(subagentReg.byID, oldest.TaskID)
	}
}

// subagentRegistryUpdate applies fn to the entry with the given task_id
// (no-op when absent). New entries are auto-created so a progress line that
// races its started record still lands.
func subagentRegistryUpdate(taskID string, fn func(*subagentEntry)) {
	subagentRegistryRecord(&subagentEntry{TaskID: taskID})
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
			Type       string  `json:"type"`
			TaskID     string  `json:"task_id,omitempty"`
			PID        int     `json:"pid,omitempty"`
			Goal       string  `json:"goal,omitempty"`
			Status     string  `json:"status,omitempty"`
			Step       int     `json:"step,omitempty"`
			Tool       string  `json:"tool,omitempty"`
			Iterations int     `json:"iterations,omitempty"`
			DurationS  float64 `json:"duration_s,omitempty"`
			TokensUsed int     `json:"tokens_used,omitempty"`
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
		case "subagent_started":
			goal := redact.RedactSecrets(rec.Goal)
			if len(goal) > maxSubagentRegistryGoalChars {
				goal = goal[:maxSubagentRegistryGoalChars]
			}
			subagentRegistryRecord(&subagentEntry{
				TaskID:    taskID,
				RunKey:    runKey,
				Goal:      goal,
				Phase:     "started",
				Status:    "running",
				PID:       rec.PID,
				StartedAt: time.Now(),
			})
		case "subagent_progress":
			subagentRegistryUpdate(taskID, func(e *subagentEntry) {
				e.Phase = "active"
				e.Step = rec.Step
				e.LastTool = rec.Tool
			})
		case "subagent_finished":
			subagentRegistryUpdate(taskID, func(e *subagentEntry) {
				e.Phase = "finished"
				e.Status = rec.Status
				e.Iterations = rec.Iterations
				e.DurationSeconds = rec.DurationS
				e.TokensUsed = rec.TokensUsed
				e.FinishedAt = time.Now()
			})
		default:
			return // tool_call/tool_result/unknown — log-only
		}

		// Fan the state transition out to the UI.
		snap := subagentRegistrySnapshot("")
		for _, e := range snap {
			if e.TaskID == taskID {
				_ = send(map[string]any{
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
				})
				break
			}
		}
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
