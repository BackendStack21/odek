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

// The per-session wake rate limit must count each wake once. dispatch
// filters the timestamp window in place into the same backing array but
// does not store the filtered slice back, so an expired entry is replaced
// by a duplicate of a live one and the next successful wake keeps it.
func TestRED_WakeRateLimitDoubleCountsAfterWindowSlides(t *testing.T) {
	router := newFakeWakeRouter(wakeIdle)
	d := newTestDispatcher(router, time.Millisecond, 3)
	defer d.Stop()

	now := time.Now()
	d.mu.Lock()
	// One wake outside the trailing hour, one inside it.
	d.wakes["sess-1"] = []time.Time{now.Add(-61 * time.Minute), now.Add(-30 * time.Minute)}
	d.pending["sess-1"] = 1
	d.mu.Unlock()

	d.dispatch("sess-1")
	if n := router.postCount(); n != 1 {
		t.Fatalf("first dispatch posted %d wakes, want 1", n)
	}

	d.mu.Lock()
	got := len(d.wakes["sess-1"])
	d.mu.Unlock()
	// Real wakes inside the trailing hour: the -30m one and the one just sent.
	if got != 2 {
		t.Fatalf("recorded wakes in the trailing hour = %d, want 2 (a live timestamp was duplicated)", got)
	}

	// With max 3/h and only 2 real wakes in the window, the next wake must go out.
	d.mu.Lock()
	d.pending["sess-1"] = 1
	d.mu.Unlock()
	d.dispatch("sess-1")
	if n := router.postCount(); n != 2 {
		t.Fatalf("second dispatch: posts=%d, want 2 (only 2 of 3 hourly wakes were used)", n)
	}
}
