package main

// Bug-hunt v3 residual fix — REST approval friction opt-in knob.
//
// With dangerous.rest_approval_friction enabled, approve/trust decisions
// through the headless REST bridge must repeat the action in a typed
// `confirm` field (the server-side friction the bridge otherwise lacks).
// Default off: auto-approving clients keep the single-field contract.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunApprovalAnswer_TypedConfirmWhenFrictionEnabled(t *testing.T) {
	prev := restApprovalFrictionEnabled
	restApprovalFrictionEnabled = true
	defer func() { restApprovalFrictionEnabled = prev }()

	run := &serveRun{ID: "run-friction"}
	registerRun(run)
	defer func() { serveRuns.mu.Lock(); delete(serveRuns.runs, run.ID); serveRuns.mu.Unlock() }()

	answer := handleRunApprovalAnswer()

	// approve without confirm: refused by the friction gate (fires before
	// the approver lookup — no live run machinery needed).
	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-friction/approvals/a1", strings.NewReader(`{"action":"approve"}`))
	w := httptest.NewRecorder()
	answer(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("approve without confirm under friction = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "confirm") {
		t.Fatalf("friction refusal should name the confirm field, got: %s", w.Body.String())
	}

	// approve with mismatched confirm: refused too.
	req = httptest.NewRequest(http.MethodPost, "/api/runs/run-friction/approvals/a1", strings.NewReader(`{"action":"approve","confirm":"trust"}`))
	w = httptest.NewRecorder()
	answer(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("approve with mismatched confirm = %d, want 409", w.Code)
	}

	// deny stays single-field: it must pass the friction gate (and land on
	// the run-level "no approver" conflict instead).
	req = httptest.NewRequest(http.MethodPost, "/api/runs/run-friction/approvals/a1", strings.NewReader(`{"action":"deny"}`))
	w = httptest.NewRecorder()
	answer(w, req)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "confirm") {
		t.Fatalf("deny should bypass the friction gate (got %d: %s)", w.Code, w.Body.String())
	}
}

func TestRunApprovalAnswer_DefaultAllowsSingleField(t *testing.T) {
	prev := restApprovalFrictionEnabled
	restApprovalFrictionEnabled = false
	defer func() { restApprovalFrictionEnabled = prev }()

	run := &serveRun{ID: "run-nofriction"}
	registerRun(run)
	defer func() { serveRuns.mu.Lock(); delete(serveRuns.runs, run.ID); serveRuns.mu.Unlock() }()

	answer := handleRunApprovalAnswer()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-nofriction/approvals/a1", strings.NewReader(`{"action":"approve"}`))
	w := httptest.NewRecorder()
	answer(w, req)
	// Friction off: the single-field approve passes the gate and reaches
	// the approver stage (nil approver here → "no approver" conflict, NOT
	// the confirm complaint).
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "confirm") {
		t.Fatalf("default posture changed: got %d (%s), want the gate to stay silent without the knob", w.Code, w.Body.String())
	}
}
