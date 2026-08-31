package main

import (
	"encoding/json"
	"sync"
	"testing"
)

// Wire v2 (P2/P6) — queued phase + cost sub-total. These pin the
// delegate_tasks pre-spawn emission path and the /api/usage sub-agent
// cost sub-total introduced with the sub-agent wire contract.

// TestSubagentRegistry_QueuedPhaseFlow pins the queued → started → active
// phase progression through the telemetry relay: a parent-synthesized
// subagent_queued record records the DECLARED profile/max_risk, the child's
// started record overwrites them with the effective post-clamp values, and
// every transition fans out a subagent_state frame carrying the wire-v2
// identity fields.
func TestSubagentRegistry_QueuedPhaseFlow(t *testing.T) {
	var mu sync.Mutex
	var frames []map[string]any
	send := func(v any) error {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := v.(map[string]any); ok && m["type"] == "subagent_state" {
			// The relay fans log lines through the same send path; only the
			// state transitions are under test here.
			frames = append(frames, m)
		}
		return nil
	}
	relay := newSubagentTelemetryRelay(send, "run-q1")

	feed := func(line string) {
		t.Helper()
		relay(0, "task-q1", line)
	}

	// Parent-side queued record (synthesized by emitSubagentQueued).
	feed(`{"type":"subagent_queued","task_id":"task-q1","goal":"audit the auth flow","profile":"reviewer","max_risk":"read_only"}`)
	// Child-side started record with EFFECTIVE post-clamp identity.
	feed(`{"type":"subagent_started","task_id":"task-q1","goal":"audit the auth flow","pid":4242,"profile":"default","max_risk":"local_write","budget_seconds":120,"budget_iterations":15,"budget_cost_usd":0.5}`)
	// Child-side progress with cumulative cost.
	feed(`{"type":"subagent_progress","task_id":"task-q1","step":3,"tool":"read_file","iterations":3,"cost_usd":0.02}`)

	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3 (queued/started/progress)", len(frames))
	}
	q, s, a := frames[0], frames[1], frames[2]

	if q["phase"] != "queued" {
		t.Errorf("queued frame phase = %v, want queued", q["phase"])
	}
	if q["goal"] != "audit the auth flow" || q["profile"] != "reviewer" || q["max_risk"] != "read_only" {
		t.Errorf("queued identity = %v/%v/%v, want declared values", q["goal"], q["profile"], q["max_risk"])
	}
	if s["phase"] != "started" {
		t.Errorf("started frame phase = %v, want started", s["phase"])
	}
	// Effective (post-clamp) identity must overwrite the declared values.
	if s["profile"] != "default" || s["max_risk"] != "local_write" {
		t.Errorf("started identity = %v/%v, want effective default/local_write", s["profile"], s["max_risk"])
	}
	if s["budget_seconds"] != 120 || s["budget_iterations"] != 15 {
		t.Errorf("started budgets = %v/%v, want 120/15", s["budget_seconds"], s["budget_iterations"])
	}
	if s["budget_cost_usd"] != 0.5 {
		t.Errorf("started budget_cost_usd = %v, want 0.5", s["budget_cost_usd"])
	}
	if a["phase"] != "active" {
		t.Errorf("progress frame phase = %v, want active", a["phase"])
	}
	if a["cost_usd"] != 0.02 {
		t.Errorf("progress cost_usd = %v, want 0.02 (cumulative)", a["cost_usd"])
	}

	// Registry snapshot carries the same truth (reload half).
	for _, e := range subagentRegistrySnapshot("run-q1") {
		if e.TaskID != "task-q1" {
			continue
		}
		if e.Phase != "active" || e.Profile != "default" || e.MaxRisk != "local_write" ||
			e.BudgetSeconds != 120 || e.BudgetIterations != 15 || e.CostUSD != 0.02 {
			t.Errorf("snapshot entry = %+v", e)
		}
		return
	}
	t.Fatal("task-q1 missing from registry snapshot")
}

func TestSubagentRegistry_QueuedFinishedCarriesCostAndArtifacts(t *testing.T) {
	var got []map[string]any
	relay := newSubagentTelemetryRelay(func(v any) error {
		if m, ok := v.(map[string]any); ok {
			got = append(got, m)
		}
		return nil
	}, "run-q2")

	relay(1, "task-q2", `{"type":"subagent_started","task_id":"task-q2","goal":"g","pid":7}`)
	relay(1, "task-q2", `{"type":"subagent_finished","task_id":"task-q2","status":"success","iterations":9,"duration_s":12.5,"tokens_used":4200,"cost_usd":0.42,"artifacts":[{"id":"report","path":"file:///tmp/report.md","bytes":2048}]}`)

	var fin map[string]any
	for _, m := range got {
		if m["phase"] == "finished" {
			fin = m
		}
	}
	if fin == nil {
		t.Fatal("no finished frame emitted")
	}
	if fin["cost_usd"] != 0.42 {
		t.Errorf("finished cost_usd = %v, want 0.42", fin["cost_usd"])
	}
	arts, ok := fin["artifacts"].([]subagentArtifact)
	if !ok || len(arts) != 1 {
		t.Fatalf("finished artifacts = %v (%T), want one subagentArtifact entry", fin["artifacts"], fin["artifacts"])
	}
	if arts[0].ID != "report" || arts[0].Bytes != 2048 {
		t.Errorf("artifact[0] = %+v, want id=report bytes=2048", arts[0])
	}

	for _, e := range subagentRegistrySnapshot("run-q2") {
		if e.TaskID == "task-q2" && (e.CostUSD != 0.42 || len(e.Artifacts) != 1) {
			t.Errorf("snapshot entry cost/artifacts = %v/%v, want 0.42/1", e.CostUSD, len(e.Artifacts))
		}
	}
}

func TestDelegateTasks_EmitSubagentQueued(t *testing.T) {
	var lines []string
	tt := &delegateTasksTool{
		OnSubagentLog: func(taskIdx int, taskID, line string) {
			lines = append(lines, line)
		},
	}
	tt.emitSubagentQueued(2, "task-q3", "audit the auth flow", "reviewer", "read_only")
	if len(lines) != 1 {
		t.Fatalf("emitted %d records, want 1", len(lines))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("record not JSON: %v", err)
	}
	if m["type"] != "subagent_queued" || m["goal"] != "audit the auth flow" ||
		m["profile"] != "reviewer" || m["max_risk"] != "read_only" {
		t.Errorf("queued record = %v", m)
	}
	// Correlation rides the positional taskID argument (the relay binds it),
	// so the record itself only needs the identity fields.

	// Nil relay (bare-struct tests, non-serve runs) must be a no-op.
	bare := &delegateTasksTool{}
	bare.emitSubagentQueued(0, "task-q4", "g", "", "")
}
