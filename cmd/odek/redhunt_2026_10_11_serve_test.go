package main

import (
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// serveFixNewRun builds a REST run wired to a wsApprover exactly like
// startServeRun does: typed approvalRequest values go through
// recordApprovalRequest, map frames through record.
func serveFixNewRun() (*serveRun, *wsApprover) {
	run := &serveRun{ID: "run-redhunt", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	approver := newWSApprover(func(v any) error {
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		return run.record(v)
	})
	run.approver = approver
	return run, approver
}

// An approval that times out on a headless REST run must leave the run's
// pending list and stop reporting waiting_approval: the loop has already
// moved on (the tool was denied by timeout) and answering the stale id
// returns 404.
func TestRED_RunApprovalExpiryLeavesStaleWaitingApproval(t *testing.T) {
	run, approver := serveFixNewRun()
	approver.SetApprovalTimeout(50 * time.Millisecond)

	err := approver.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
	if err == nil {
		t.Fatal("PromptCommand returned nil; want an approval timeout error")
	}

	snap := run.snapshot(false)
	pending, _ := snap["pending_approvals"].([]map[string]any)
	if snap["status"] != "running" || len(pending) != 0 {
		t.Fatalf("after approval expiry: status=%v pending=%d; want status=running and no pending approvals (the approval is gone: answering it returns 404)", snap["status"], len(pending))
	}
}
