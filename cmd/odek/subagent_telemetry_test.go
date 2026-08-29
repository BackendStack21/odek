package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
)

// ── scanSubagentStream: protocol 2 ───────────────────────────────────

// Protocol-2 children emit lifecycle records, tool events, and ONE framed
// result line. All lifecycle lines must forward via onLog; the framed
// result's inner map is the task result.
func TestScanSubagentStream_Protocol2_LifecycleAndFramedResult(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"subagent_started","task_id":"task-01","pid":42,"depth":1,"timeout_s":1800,"max_iter":15}`,
		`{"type":"tool_call","name":"read_file","data":"{\"path\":\"a.go\"}","task_id":"task-01"}`,
		`{"type":"subagent_progress","task_id":"task-01","step":1,"tool":"read_file","elapsed_s":2}`,
		`{"type":"tool_result","name":"read_file","data":"package main","task_id":"task-01"}`,
		`{"type":"subagent_finished","task_id":"task-01","status":"success","iterations":4,"duration_s":12.5}`,
		`{"type":"result","task_id":"task-01","result":{"status":"success","summary":"done","iterations":4}}`,
	}, "\n")

	var logged []string
	result, _, err := scanSubagentStream(strings.NewReader(input), func(line string) {
		logged = append(logged, line)
	})
	if err != nil {
		t.Fatalf("scanSubagentStream: %v", err)
	}
	if len(logged) != 5 {
		t.Errorf("forwarded %d progress lines, want 5 (started, tool_call, progress, tool_result, finished)", len(logged))
	}
	if result == nil {
		t.Fatal("result is nil — framed result not detected")
	}
	if got := result["status"]; got != "success" {
		t.Errorf("result[status] = %v, want success (inner map unwrapped)", got)
	}
	if got := result["summary"]; got != "done" {
		t.Errorf("result[summary] = %v, want done", got)
	}
	if _, has := result["type"]; has {
		t.Errorf("result map should be the inner object without the type wrapper, got %v", result)
	}
}

// Unknown typed lines (version skew: newer child, older parent) are
// protocol traffic — forwarded as progress, NEVER treated as the result.
func TestScanSubagentStream_UnknownTypeNeverBecomesResult(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"subagent_started","task_id":"t"}`,
		`{"type":"future_record_from_newer_child","payload":{"huge":"blob"}}`,
	}, "\n")

	var logged int
	result, _, err := scanSubagentStream(strings.NewReader(input), func(line string) { logged++ })
	if err != nil {
		t.Fatalf("scanSubagentStream: %v", err)
	}
	if logged != 2 {
		t.Errorf("forwarded %d lines, want 2", logged)
	}
	if result != nil {
		t.Errorf("unknown typed line must never become the result, got %v", result)
	}
}

// Legacy children (no type field) write a bare compact JSON result —
// unchanged behavior.
func TestScanSubagentStream_LegacyUntypedResult(t *testing.T) {
	input := `{"status":"success","summary":"legacy","iterations":2}`
	result, _, err := scanSubagentStream(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("scanSubagentStream: %v", err)
	}
	if result == nil || result["summary"] != "legacy" {
		t.Errorf("legacy result not parsed, got %v", result)
	}
}

// A partial line from a killed child must not poison the result.
func TestScanSubagentStream_PartialLineIgnored(t *testing.T) {
	input := `{"type":"subagent_started","task_id":"t"}` + "\n" + `{"status":"suc`
	var logged int
	result, _, err := scanSubagentStream(strings.NewReader(input), func(line string) { logged++ })
	if err != nil {
		t.Fatalf("scanSubagentStream: %v", err)
	}
	if logged != 1 || result != nil {
		t.Errorf("logged=%d result=%v, want 1 forwarded line and nil result", logged, result)
	}
}

// ── Task envelope: task_id + protocol ─────────────────────────────────

func TestTaskEnvelope_CarriesTaskIDAndProtocol(t *testing.T) {
	env := newTaskEnvelope("task-a", "goal", "", "", "trusted", "", "", nil, "trusted")
	if env.TaskID == "" {
		t.Fatal("envelope TaskID is empty — parent must stamp a correlation id")
	}
	if env.Protocol != subagentProtocolV2 {
		t.Errorf("envelope Protocol = %d, want %d", env.Protocol, subagentProtocolV2)
	}
	if other := newTaskEnvelope("task-b", "goal", "", "", "", "", "", nil, ""); other.TaskID == env.TaskID {
		t.Error("two envelopes share a task_id — correlation ids must be unique")
	}
}

// ── Parent lifecycle events ──────────────────────────────────────────

func TestSubagentSpawnedEvent_FieldsAndGoalPrivacy(t *testing.T) {
	ev := subagentSpawnedEvent("task-abc", 4242, 1, 1800, "secret goal text about prod")
	if ev.Type != events.TypeSubagentSpawned {
		t.Errorf("type = %q, want %q", ev.Type, events.TypeSubagentSpawned)
	}
	if ev.Data["task_id"] != "task-abc" || ev.Data["pid"] != 4242 {
		t.Errorf("missing correlation fields: %v", ev.Data)
	}
	if _, ok := ev.Data["goal_sha256"]; !ok {
		t.Error("goal_sha256 missing — dashboards cannot correlate without it")
	}
	if s, _ := ev.Data["goal"].(string); strings.Contains(s, "secret goal") {
		t.Error("plaintext goal leaked into event data — hash only")
	}
}

func TestSubagentCompletedEvent_StatusAndUsage(t *testing.T) {
	res := map[string]any{
		"status":      "success",
		"iterations":  float64(4),
		"tokens_used": float64(1234),
	}
	ev := subagentCompletedEvent("task-abc", res, "completed")
	if ev.Type != events.TypeSubagentCompleted {
		t.Errorf("type = %q, want %q", ev.Type, events.TypeSubagentCompleted)
	}
	if ev.Data["status"] != "success" || ev.Data["task_id"] != "task-abc" {
		t.Errorf("bad data: %v", ev.Data)
	}
}

func TestSubagentCompletedEvent_NilResultUsesFallbackStatus(t *testing.T) {
	ev := subagentCompletedEvent("task-abc", nil, "timeout")
	if ev.Data["status"] != "timeout" {
		t.Errorf("status = %v, want timeout fallback", ev.Data["status"])
	}
}

func TestSubagentExitStatus_PriorityOrder(t *testing.T) {
	ctx := t.Context()
	if got := subagentExitStatus(map[string]any{"status": "partial"}, nil, ctx, nil); got != "partial" {
		t.Errorf("child status should win, got %q", got)
	}
	if got := subagentExitStatus(nil, nil, ctx, nil); got != "no_result" {
		t.Errorf("clean exit without result should be no_result, got %q", got)
	}
}

// ── Serve relay: redact + cap (M1 step 0) ────────────────────────────

func TestSubagentLogRelay_RedactsAndCapsAndCorrelates(t *testing.T) {
	var got map[string]any
	relay := newSubagentLogRelay(func(v any) error {
		got = v.(map[string]any)
		return nil
	})

	// Runtime-constructed credential (GitHub PAT shape) so the payload
	// genuinely contains a secret the redactor must catch.
	secret := "ghp_" + strings.Repeat("a", 36)
	huge := strings.Repeat("token="+secret+" ", 1000) // oversized + credential-bearing
	line, _ := json.Marshal(map[string]string{
		"type": "tool_result",
		"name": "shell",
		"data": huge,
	})
	relay(2, "task-xyz", string(line))

	if got == nil {
		t.Fatal("relay dropped the message entirely")
	}
	if got["task_id"] != "task-xyz" || got["task_idx"] != 2 {
		t.Errorf("correlation fields missing: %v", got)
	}
	data, _ := got["data"].(string)
	if strings.Contains(data, secret) {
		t.Error("credential leaked through the subagent_log relay unredacted")
	}
	if len(data) > maxSubagentRelayDataBytes {
		t.Errorf("data len %d exceeds cap %d", len(data), maxSubagentRelayDataBytes)
	}
}

func TestSubagentLogRelay_MalformedLineDropped(t *testing.T) {
	called := false
	relay := newSubagentLogRelay(func(any) error {
		called = true
		return nil
	})
	relay(0, "task-1", "not json at all")
	if called {
		t.Error("malformed child line must be dropped, not relayed")
	}
}

// ── Child telemetry records ──────────────────────────────────────────

func TestEmitSubagentTelemetry_CompactNDJSONWithTaskID(t *testing.T) {
	var buf strings.Builder
	tw := newSubagentTelemetryWriter(&buf, "task-77")
	if tw == nil {
		t.Fatal("writer is nil")
	}
	tw.emit(map[string]any{"type": "subagent_started", "pid": 7})

	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatal("telemetry record missing trailing newline")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("record is not valid JSON: %v (%q)", err, line)
	}
	if m["task_id"] != "task-77" {
		t.Errorf("task_id = %v, want task-77 (echoed on every record)", m["task_id"])
	}
	if strings.Count(line, "\n") != 1 {
		t.Error("record must be a single compact line")
	}
}

func TestNewSubagentTelemetryWriter_NilWithoutTaskID(t *testing.T) {
	if newSubagentTelemetryWriter(&strings.Builder{}, "") != nil {
		t.Error("writer must be nil without a task_id (standalone runs stay silent)")
	}
}
