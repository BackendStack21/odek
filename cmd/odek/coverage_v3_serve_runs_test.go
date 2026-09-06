package main

// Coverage v3 — handleRunApprovalAnswer residual branches: method gate,
// bad path shape, unknown run, invalid JSON, bad action, unknown approval
// id (HandleResponse false), and the success path with a live pending
// approval fed through a real wsApprover + serveRun.record pipeline.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

func registerAnswerTestRun(t *testing.T, id string) *serveRun {
	t.Helper()
	r := &serveRun{
		ID:      id,
		Status:  "running",
		pending: map[string]*approvalRequest{},
	}
	r.cond = sync.NewCond(&r.mu)
	registerRun(r)
	t.Cleanup(func() {
		serveRuns.mu.Lock()
		delete(serveRuns.runs, r.ID)
		serveRuns.mu.Unlock()
	})
	return r
}

func postAnswer(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	handleRunApprovalAnswer()(w, req)
	return w
}

func TestRunApprovalAnswer_MethodAndPathBranches(t *testing.T) {
	registerAnswerTestRun(t, "run-ans-1")

	// Method gate.
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-ans-1/approvals/a1", nil)
	w := httptest.NewRecorder()
	handleRunApprovalAnswer()(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", w.Code)
	}

	// Bad path shape (missing approval id segment).
	if w := postAnswer(t, "/api/runs/run-ans-1/approvals", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("short path = %d, want 400", w.Code)
	}
	// Bad path shape (wrong middle segment).
	if w := postAnswer(t, "/api/runs/run-ans-1/other/a1", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong segment = %d, want 400", w.Code)
	}
	// Unknown run.
	if w := postAnswer(t, "/api/runs/run-nope/approvals/a1", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown run = %d, want 404", w.Code)
	}
	// Invalid JSON.
	if w := postAnswer(t, "/api/runs/run-ans-1/approvals/a1", `{nope`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400", w.Code)
	}
	// Invalid action.
	if w := postAnswer(t, "/api/runs/run-ans-1/approvals/a1", `{"action":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad action = %d, want 400", w.Code)
	}
}

func TestRunApprovalAnswer_UnknownApprovalID(t *testing.T) {
	prev := restApprovalFrictionEnabled
	restApprovalFrictionEnabled = false
	defer func() { restApprovalFrictionEnabled = prev }()

	run := registerAnswerTestRun(t, "run-ans-2")
	approver := newWSApprover(run.record)
	run.mu.Lock()
	run.approver = approver
	run.mu.Unlock()

	// Approver exists but no approval with this id is pending.
	w := postAnswer(t, "/api/runs/run-ans-2/approvals/missing", `{"action":"deny"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown approval = %d, want 404 (got body %s)", w.Code, w.Body.String())
	}
}

func TestRunApprovalAnswer_SuccessDeliversThroughApprover(t *testing.T) {
	prev := restApprovalFrictionEnabled
	restApprovalFrictionEnabled = false
	defer func() { restApprovalFrictionEnabled = prev }()

	run := registerAnswerTestRun(t, "run-ans-3")
	approver := newWSApprover(func(v any) error {
		// Mirror the serve wiring: typed approvalRequest frames go to the
		// pending map, everything else to the event sink.
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		return run.record(v)
	})
	approver.SetApprovalTimeout(3 * time.Second)
	run.mu.Lock()
	run.approver = approver
	run.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- approver.PromptCommand(danger.LocalWrite, "echo hi", "test")
	}()

	// Wait until the approval request lands in the run's pending map.
	var approvalID string
	deadline := time.Now().Add(5 * time.Second)
	for {
		run.mu.Lock()
		for id := range run.pending {
			approvalID = id
		}
		n := len(run.pending)
		run.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if approvalID == "" {
		t.Fatal("approval request never reached the run's pending map")
	}

	w := postAnswer(t, "/api/runs/run-ans-3/approvals/"+approvalID, `{"action":"approve"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp struct {
		RunID      string `json:"run_id"`
		ApprovalID string `json:"approval_id"`
		Action     string `json:"action"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Action != "approve" || resp.ApprovalID != approvalID {
		t.Fatalf("unexpected ack body: %+v", resp)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PromptCommand after approve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PromptCommand never returned after approval")
	}

	// The ack must have cleared the pending map and reset the status.
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.pending) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(run.pending))
	}
	if run.Status != "running" {
		t.Fatalf("status = %q, want running", run.Status)
	}
}
