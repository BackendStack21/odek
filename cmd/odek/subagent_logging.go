package main

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/runtimelog"
)

func (t *delegateTasksTool) SetEventContext(c events.Context) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	t.eventContext = c
}
func (t *delegateTasksTool) childEventContext(taskID string) events.Context {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	c := t.eventContext
	return events.Context{RootRunID: c.RootRunID, ParentRunID: c.RunID, ParentTurnID: c.TurnID, SessionID: c.SessionID, TaskID: taskID, ParentTaskID: c.TaskID}
}

// subagentActivity tracks observations, not inferred health. Pending calls can
// be waiting for approval/concurrency; only executing records mark activity.
type subagentActivity struct {
	mu      sync.Mutex
	last    time.Time
	llm     bool
	pending map[string]bool
	active  map[string]bool
}

func newSubagentActivity() *subagentActivity {
	return &subagentActivity{last: time.Now(), pending: map[string]bool{}, active: map[string]bool{}}
}
func (a *subagentActivity) observe(ev events.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = time.Now()
	id, _ := ev.Data["call_id"].(string)
	switch ev.Type {
	case "llm_call_started":
		a.llm = true
	case "llm_call_completed", "llm_call_failed":
		a.llm = false
	case "tool_call_started":
		a.pending[id] = true
	case "tool_call_executing":
		delete(a.pending, id)
		a.active[id] = true
	case "tool_call_completed", "tool_call_failed", "tool_execution_completed":
		delete(a.pending, id)
		delete(a.active, id)
	}
}
func (a *subagentActivity) snapshot(start time.Time) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	activity := "process_running"
	if a.llm {
		activity = "llm_request"
	}
	if len(a.pending) > 0 {
		activity = "tools_pending"
	}
	if len(a.active) > 0 {
		activity = "tool_execution"
	}
	return map[string]any{"activity": activity, "active_calls": len(a.active), "pending_calls": len(a.pending), "elapsed_seconds": time.Since(start).Seconds(), "last_event_age_seconds": time.Since(a.last).Seconds()}
}

// relayRuntimeRecord never persists raw child log lines. Only a recognized,
// metadata-only event is forwarded; the parent owns session/root correlation.
func (t *delegateTasksTool) relayRuntimeRecord(taskID, line string, a *subagentActivity) bool {
	var rec struct {
		Type  string       `json:"type"`
		Event events.Event `json:"event"`
	}
	if json.Unmarshal([]byte(line), &rec) != nil {
		return false
	}
	if rec.Type == "subagent_started" {
		var meta struct {
			PID              int     `json:"pid"`
			Profile          string  `json:"profile"`
			MaxRisk          string  `json:"max_risk"`
			BudgetSeconds    int     `json:"budget_seconds"`
			BudgetIterations int     `json:"budget_iterations"`
			BudgetCost       float64 `json:"budget_cost_usd"`
		}
		if json.Unmarshal([]byte(line), &meta) == nil {
			t.emitSubagentEvent(events.Event{Type: "subagent_started", TaskID: taskID, Data: map[string]any{"pid": meta.PID, "profile": meta.Profile, "max_risk": meta.MaxRisk, "budget_seconds": meta.BudgetSeconds, "budget_iterations": meta.BudgetIterations, "budget_cost_usd": meta.BudgetCost}})
		}
		return false // the existing UI relay still receives this lifecycle frame
	}
	if rec.Type != "runtime_event" {
		return false
	}
	ev, ok := runtimelog.Sanitize(rec.Event)
	if !ok {
		return true
	}
	c := t.childEventContext(taskID)
	ev.SourceTaskID = taskID
	ev.SessionID = c.SessionID
	ev.RootRunID = c.RootRunID
	if ev.TaskID == "" || ev.TaskID == taskID {
		ev.TaskID = taskID
		ev.ParentTaskID = c.ParentTaskID
		ev.ParentRunID = c.ParentRunID
		ev.ParentTurnID = c.ParentTurnID
		a.observe(ev)
	}
	t.emitSubagentEvent(ev)
	return true
}

// boundedStderr counts diagnostics without retaining private child output.
// The default runtime log records only the byte count; raw text never enters it.
type boundedStderr struct{ n int64 }

func (b *boundedStderr) Write(p []byte) (int, error) { b.n += int64(len(p)); return len(p), nil }

var _ io.Writer = (*boundedStderr)(nil)

func (t *delegateTasksTool) monitorSubagent(taskID string, a *subagentActivity, interval time.Duration) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				t.emitSubagentEvent(events.Event{Type: "subagent_running", TaskID: taskID, Data: a.snapshot(start)})
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop); <-done }) }
}
