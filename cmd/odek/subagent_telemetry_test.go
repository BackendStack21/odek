package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
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

// ── Wire additions (P1/P3 — child half) ──────────────────────────────

// wireHarness builds a writer with a fully configured wire context and a
// usage probe that counts consultations (the cost fields must be derived
// from the engine's cumulative totals, not guessed).
func wireHarness(t *testing.T, buf *bytes.Buffer) (*subagentTelemetryWriter, *int) {
	t.Helper()
	calls := 0
	tw := newSubagentTelemetryWriterWithWire(buf, "task-w1", subagentWireContext{
		Profile: "reviewer",
		MaxRisk: "local_write",
		Budget: newSubagentWireBudget(
			budget.Limits{MaxCostUSD: 0.5, InputCostPerMillionUSD: 1.5, OutputCostPerMillionUSD: 7.5},
			120, 15,
		),
		Cost: subagentCostEstimator{inPerMillion: 1.5, outPerMillion: 7.5},
		Usage: func() (int64, int64) {
			calls++
			return 2_000_000, 1_000_000 // 2M in, 1M out → 2*1.5 + 1*7.5 = 10.5
		},
	})
	if tw == nil {
		t.Fatal("wire writer is nil")
	}
	return tw, &calls
}

func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := buf.String()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &m); err != nil {
		t.Fatalf("record is not valid JSON: %v (%q)", err, line)
	}
	return m
}

// P1: the started record carries the resolved profile id, the effective
// post-clamp risk cap, and the P3 budget block. cost_usd is deliberately
// absent — nothing has been spent at start.
func TestSubagentWire_StartedCarriesProfileRiskAndBudgets(t *testing.T) {
	var buf bytes.Buffer
	tw, _ := wireHarness(t, &buf)
	tw.emitStarted(4242, 1, 120, 15)

	m := decodeRecord(t, &buf)
	if m["task_id"] != "task-w1" {
		t.Errorf("task_id = %v, want task-w1", m["task_id"])
	}
	if m["profile"] != "reviewer" {
		t.Errorf("profile = %v, want reviewer (resolved profile id)", m["profile"])
	}
	if m["max_risk"] != "local_write" {
		t.Errorf("max_risk = %v, want local_write (effective post-clamp cap)", m["max_risk"])
	}
	if m["budget_seconds"] != float64(120) {
		t.Errorf("budget_seconds = %v, want 120", m["budget_seconds"])
	}
	if m["budget_iterations"] != float64(15) {
		t.Errorf("budget_iterations = %v, want 15", m["budget_iterations"])
	}
	if m["budget_cost_usd"] != 0.5 {
		t.Errorf("budget_cost_usd = %v, want 0.5 (enforced cap)", m["budget_cost_usd"])
	}
	if _, has := m["cost_usd"]; has {
		t.Error("started record must not carry cost_usd (nothing spent at start)")
	}
}

// All-zero wire context (no profile, no cap, unconfigured budgets) must
// omit every new field — an absent field means "not configured", never 0.
func TestSubagentWire_StartedOmitsUnconfiguredWireFields(t *testing.T) {
	var buf bytes.Buffer
	tw := newSubagentTelemetryWriterWithWire(&buf, "task-w2", subagentWireContext{})
	if tw == nil {
		t.Fatal("writer is nil")
	}
	tw.emitStarted(7, 0, 0, 0)

	m := decodeRecord(t, &buf)
	for _, k := range []string{"profile", "max_risk", "budget_seconds", "budget_iterations", "budget_cost_usd"} {
		if _, has := m[k]; has {
			t.Errorf("%s must be omitted when unconfigured, got %v", k, m[k])
		}
	}
}

// P3: progress records carry the cumulative cost estimate (the same
// /api/usage math over the engine's provider-reported totals) plus the
// budget block, so a client can render % budget used per step.
func TestSubagentWire_ProgressCarriesCostSoFarAndBudgets(t *testing.T) {
	var buf bytes.Buffer
	tw, calls := wireHarness(t, &buf)
	tw.emitProgress("read_file")

	m := decodeRecord(t, &buf)
	if m["step"] != float64(1) || m["tool"] != "read_file" {
		t.Errorf("progress core fields wrong: %v", m)
	}
	if m["cost_usd"] != 10.5 {
		t.Errorf("cost_usd = %v, want 10.5 (2M in × $1.5/M + 1M out × $7.5/M)", m["cost_usd"])
	}
	if m["budget_seconds"] != float64(120) || m["budget_iterations"] != float64(15) || m["budget_cost_usd"] != 0.5 {
		t.Errorf("progress budget block wrong: %v", m)
	}
	if *calls != 1 {
		t.Errorf("usage probe consulted %d times, want 1 (cost derived from engine totals)", *calls)
	}
}

// Without configured prices the cumulative cost is unknowable — the wire
// omits cost_usd entirely rather than emitting a fabricated $0.
func TestSubagentWire_ProgressOmitsCostWhenPricesAbsent(t *testing.T) {
	var buf bytes.Buffer
	tw := newSubagentTelemetryWriterWithWire(&buf, "task-w3", subagentWireContext{
		Budget: subagentWireBudget{Seconds: 60, Iterations: 10},
		Usage:  func() (int64, int64) { return 999, 999 },
	})
	tw.emitProgress("shell")

	m := decodeRecord(t, &buf)
	if _, has := m["cost_usd"]; has {
		t.Errorf("cost_usd must be omitted without configured prices, got %v", m["cost_usd"])
	}
	if m["budget_seconds"] != float64(60) || m["budget_iterations"] != float64(10) {
		t.Errorf("budget block must survive without prices: %v", m)
	}
}

// The terminal record carries the FINAL cost estimate alongside the
// existing status/iterations/duration/tokens fields.
func TestSubagentWire_FinishedCarriesFinalCost(t *testing.T) {
	var buf bytes.Buffer
	tw := newSubagentTelemetryWriterWithWire(&buf, "task-w4", subagentWireContext{
		Cost:   subagentCostEstimator{inPerMillion: 1.5, outPerMillion: 7.5},
		Usage:  func() (int64, int64) { return 3_000_000, 1_000_000 }, // 4.5 + 7.5 = 12
	})
	tw.emitFinished("success", 4, 12.5, 900)

	m := decodeRecord(t, &buf)
	if m["status"] != "success" || m["iterations"] != float64(4) || m["duration_s"] != 12.5 || m["tokens_used"] != float64(900) {
		t.Errorf("finished core fields wrong: %v", m)
	}
	if m["cost_usd"] != 12.0 {
		t.Errorf("cost_usd = %v, want 12 (3M in × $1.5/M + 1M out × $7.5/M)", m["cost_usd"])
	}
}

func TestSubagentWire_FinishedOmitsCostWithoutPrices(t *testing.T) {
	var buf bytes.Buffer
	tw := newSubagentTelemetryWriterWithWire(&buf, "task-w5", subagentWireContext{})
	tw.emitFinished("error", 0, 1, 1)

	m := decodeRecord(t, &buf)
	if _, has := m["cost_usd"]; has {
		t.Error("cost_usd must be omitted without configured prices")
	}
}

// The estimator must reproduce the /api/usage math exactly: prices
// resolved for the run's model (per-model entry overriding the flat pair
// per field), spend = in/1e6·inPrice + out/1e6·outPrice, and configured()
// mirroring handleUsage's prices_configured predicate.
func TestSubagentCostEstimator_MatchesUsageAPIEstimate(t *testing.T) {
	limits := budget.Limits{
		InputCostPerMillionUSD:  1,
		OutputCostPerMillionUSD: 2,
		ModelPrices:             map[string]budget.ModelPrice{"m-fast": {InputCostPerMillionUSD: 5}},
	}
	est := newSubagentCostEstimator(limits, "m-fast")
	const in, out = 2_000_000, 3_000_000
	want := limits.ResolveForModel("m-fast").EstimatedCostUSD(in, out)
	if got := est.estimate(in, out); got != want {
		t.Errorf("estimate = %v, want %v (must equal the /api/usage math)", got, want)
	}
	if want != 16.0 {
		t.Errorf("sanity: want 16 (2×5 + 3×2), got %v", want)
	}
	if !est.configured() {
		t.Error("configured() = false with resolved prices, want true")
	}
	if inP, outP := limits.ResolvePrices("m-fast"); !(inP > 0 || outP > 0) != !est.configured() {
		t.Error("configured() must mirror handleUsage's prices_configured predicate")
	}
	if (newSubagentCostEstimator(budget.Limits{}, "m-fast")).configured() {
		t.Error("configured() = true with no prices, want false")
	}
}

// A cost cap without configured prices is NOT enforced (budget contract) —
// the wire must not report it as one.
func TestSubagentWireBudget_CostCapWithoutPricesNeverEmitted(t *testing.T) {
	b := newSubagentWireBudget(budget.Limits{MaxCostUSD: 0.5}, 60, 10)
	f := b.fields()
	if _, has := f["budget_cost_usd"]; has {
		t.Errorf("budget_cost_usd = %v, want omitted (CostEnforcementActive is false without prices)", f["budget_cost_usd"])
	}
	if f["budget_seconds"] != 60 || f["budget_iterations"] != 10 {
		t.Errorf("seconds/iterations lost: %v", f)
	}
}
