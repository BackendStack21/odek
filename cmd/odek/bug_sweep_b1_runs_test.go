package main

// Bug-sweep batch 1 (fix/bug-hunt-b1) — B1 regression test.
//
// RED-first: this failed against the pre-fix handler, which answered
// DELETE /api/runs/{id} with the hardcoded status "cancelled" regardless of
// the run's real (terminal) status, and re-stamped EndedAt via cancelRun.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRunDelete_TerminalRunReportsRealStatus(t *testing.T) {
	run := &serveRun{
		ID:        "run-b1-delete-terminal",
		SessionID: "sess-b1",
		Model:     "test-model",
		Status:    "running",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	run.cond = sync.NewCond(&run.mu)
	run.pending = map[string]*approvalRequest{}
	registerRun(run)
	run.finish("completed", "")
	run.mu.Lock()
	endedAt := run.EndedAt
	run.mu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/runs/run-b1-delete-terminal", nil)
	handleRunByID()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
		Idle   bool   `json:"idle"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body: %s)", err, rr.Body.String())
	}
	if resp.Status != "completed" {
		t.Errorf("response status = %q, want the run's real status %q (hardcoded-cancelled lie)", resp.Status, "completed")
	}
	if !resp.Idle {
		t.Errorf("response idle = false, want true for a terminal run")
	}
	run.mu.Lock()
	nowEnded := run.EndedAt
	run.mu.Unlock()
	if !nowEnded.Equal(endedAt) {
		t.Errorf("DELETE on terminal run re-stamped EndedAt: %v -> %v", endedAt, nowEnded)
	}
}
