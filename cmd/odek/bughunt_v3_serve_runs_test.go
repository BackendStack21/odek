package main

import (
	"sync"
	"testing"
)

func newTerminalTestRun() *serveRun {
	r := &serveRun{
		ID:       "run-terminal-guard",
		Status:   "running",
		pending:  map[string]*approvalRequest{},
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// RED: a late map-path approval_request frame after finish("cancelled") must
// not flip a terminal run back to waiting_approval nor repopulate pending.
func TestServeRunRecordApprovalRequestAfterFinishStaysTerminal(t *testing.T) {
	r := newTerminalTestRun()
	r.finish("cancelled", "")

	_ = r.record(map[string]any{
		"type":    "approval_request",
		"id":      "appr-late-1",
		"risk":    "local_write",
		"command": "echo hi",
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	if !runStatusTerminal(r.Status) {
		t.Fatalf("status = %q, want terminal", r.Status)
	}
	if len(r.pending) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(r.pending))
	}
}

// RED: a late typed approvalRequest after finish("cancelled") must not
// repopulate the pending_approvals snapshot of a terminal run.
func TestServeRunRecordTypedApprovalRequestAfterFinishStaysTerminal(t *testing.T) {
	r := newTerminalTestRun()
	r.finish("cancelled", "")

	r.recordApprovalRequest(approvalRequest{
		ID:      "appr-late-2",
		Type:    "approval_request",
		Risk:    "local_write",
		Command: "echo hi",
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	if !runStatusTerminal(r.Status) {
		t.Fatalf("status = %q, want terminal", r.Status)
	}
	if len(r.pending) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(r.pending))
	}
}

// Snapshot must expose the empty pending list for terminal runs (regression
// coverage for the GET /api/runs/{id} surface).
func TestServeRunSnapshotAfterFinishNoPendingApprovals(t *testing.T) {
	r := newTerminalTestRun()
	r.finish("cancelled", "")
	r.recordApprovalRequest(approvalRequest{ID: "appr-late-3", Type: "approval_request"})

	snap := r.snapshot(true)
	pending, ok := snap["pending_approvals"].([]map[string]any)
	if !ok {
		t.Fatalf("pending_approvals missing or wrong type: %T", snap["pending_approvals"])
	}
	if len(pending) != 0 {
		t.Fatalf("pending_approvals = %d entries, want 0", len(pending))
	}
	if s, _ := snap["status"].(string); !runStatusTerminal(s) {
		t.Fatalf("snapshot status = %q, want terminal", s)
	}
}
