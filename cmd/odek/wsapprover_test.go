package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

func TestNewWSApprover(t *testing.T) {
	sendFn := func(v any) error { return nil }
	a := newWSApprover(sendFn)
	if a == nil {
		t.Fatal("newWSApprover returned nil")
	}
	if a.sendFn == nil {
		t.Error("sendFn should be set")
	}
	if len(a.pending) != 0 {
		t.Errorf("pending should be empty, got %d", len(a.pending))
	}
	if len(a.approveAll) != 0 {
		t.Errorf("approveAll should be empty, got %d", len(a.approveAll))
	}
}

func TestWSApprover_NewID(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	id := a.newID()
	if !strings.HasPrefix(id, "apr-") {
		t.Errorf("expected 'apr-' prefix, got %q", id)
	}
	if len(id) != 20 { // "apr-" + 16 hex chars (8 bytes)
		t.Errorf("expected length 20, got %d (%q)", len(id), id)
	}
	// Two IDs should differ
	id2 := a.newID()
	if id == id2 {
		t.Error("newID should generate unique IDs")
	}
}

func TestWSApprover_HandleResponse_Matching(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	id := "test-id-1"

	// Manually set up a pending request
	a.mu.Lock()
	a.pending[id] = make(chan string, 1)
	a.mu.Unlock()

	// Handle matching response
	ok := a.HandleResponse(id, "approve")
	if !ok {
		t.Error("HandleResponse should return true for matching ID")
	}

	// Verify the action was sent to the channel
	select {
	case action := <-a.pending[id]:
		if action != "approve" {
			t.Errorf("expected 'approve', got %q", action)
		}
	default:
		t.Error("expected action to be sent to channel")
	}
}

func TestWSApprover_HandleResponse_NonMatching(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	ok := a.HandleResponse("nonexistent", "approve")
	if ok {
		t.Error("HandleResponse should return false for non-matching ID")
	}
}

// TestWSApprover_HandleResponse_DoesNotBlock verifies that a duplicate or
// late approval response is dropped instead of blocking the WebSocket read
// goroutine. This is a regression test for the L3 relay race: previously the
// second response could wedge on a buffer-1 channel and exhaust the connection
// semaphore.
func TestWSApprover_HandleResponse_DoesNotBlock(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	id := "test-id-block"

	a.mu.Lock()
	a.pending[id] = make(chan string, 1)
	a.mu.Unlock()

	// First response fills the buffer-1 channel.
	if !a.HandleResponse(id, "approve") {
		t.Fatal("first HandleResponse should match")
	}

	// Second response must return promptly even though the channel is full.
	done := make(chan struct{})
	go func() {
		a.HandleResponse(id, "approve")
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("second HandleResponse blocked on a full response channel")
	}

	// Only one value should have been delivered.
	select {
	case action := <-a.pending[id]:
		if action != "approve" {
			t.Errorf("expected 'approve', got %q", action)
		}
	default:
		t.Fatal("expected exactly one buffered response")
	}
}

func TestWSApprover_PromptCommand_TrustedClass(t *testing.T) {
	callCount := 0
	a := newWSApprover(func(v any) error {
		callCount++
		return nil
	})

	// Set trusted class
	a.approveAll[danger.Safe] = true

	err := a.PromptCommand(danger.Safe, "ls", "test")
	if err != nil {
		t.Errorf("expected nil for trusted class, got: %v", err)
	}
	if callCount != 0 {
		t.Errorf("sendFn should not be called for trusted class, called %d times", callCount)
	}
}

func TestWSApprover_PromptCommand_SendError(t *testing.T) {
	sendErr := errors.New("websocket disconnected")
	a := newWSApprover(func(v any) error {
		return sendErr
	})

	err := a.PromptCommand(danger.Safe, "ls", "test")
	if err == nil {
		t.Fatal("expected error from send failure")
	}
	if !strings.Contains(err.Error(), "send failed") {
		t.Errorf("expected 'send failed' in error, got: %v", err)
	}
}

func TestWSApprover_PromptCommand_Approve(t *testing.T) {
	sendCalled := make(chan struct{}, 1)
	a := newWSApprover(func(v any) error {
		sendCalled <- struct{}{}
		return nil
	})

	// Run PromptCommand in a goroutine, send approval via HandleResponse
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.PromptCommand(danger.Safe, "ls", "test")
	}()

	// Wait for sendFn to be called (pending entry is guaranteed to be set)
	<-sendCalled

	// Read the pending ID
	a.mu.Lock()
	var pendingID string
	for id := range a.pending {
		pendingID = id
		break
	}
	a.mu.Unlock()

	if pendingID == "" {
		t.Fatal("expected a pending request to appear")
	}

	a.HandleResponse(pendingID, "approve")

	err := <-errCh
	if err != nil {
		t.Errorf("expected nil for approve, got: %v", err)
	}
}

func TestWSApprover_PromptCommand_Deny(t *testing.T) {
	sendCalled := make(chan struct{}, 1)
	a := newWSApprover(func(v any) error {
		sendCalled <- struct{}{}
		return nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.PromptCommand(danger.Safe, "rm -rf /", "test deny")
	}()

	// Wait for sendFn to be called
	<-sendCalled

	a.mu.Lock()
	var pendingID string
	for id := range a.pending {
		pendingID = id
		break
	}
	a.mu.Unlock()

	if pendingID == "" {
		t.Fatal("expected a pending request to appear")
	}

	a.HandleResponse(pendingID, "deny")

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for deny")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("expected 'denied' in error, got: %v", err)
	}
}

func TestWSApprover_PromptCommand_Trust(t *testing.T) {
	sendCalled := make(chan struct{}, 1)
	a := newWSApprover(func(v any) error {
		sendCalled <- struct{}{}
		return nil
	})

	cls := danger.SystemWrite
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.PromptCommand(cls, "rm /etc/hosts", "test trust")
	}()

	// Wait for sendFn to be called
	<-sendCalled

	a.mu.Lock()
	var pendingID string
	for id := range a.pending {
		pendingID = id
		break
	}
	a.mu.Unlock()

	if pendingID == "" {
		t.Fatal("expected a pending request to appear")
	}

	a.HandleResponse(pendingID, "trust")

	err := <-errCh
	if err != nil {
		t.Errorf("expected nil for trust, got: %v", err)
	}

	// Verify class is now trusted
	if !a.approveAll[cls] {
		t.Error("expected SystemWrite to be trusted after trust response")
	}
}

// TestWSApprover_PromptCommand_TrustToolBatchNotCachable audits the 2026-08
// finding that the synthetic tool_batch class was trustable in the WS
// approver (the Telegram approver had already been fixed): one Trust click
// on a batch card cached approveAll["tool_batch"], auto-passing every later
// batch — and via the loop's SetTrustAll, every per-tool prompt in the
// session. A "trust" response for a batch must coerce to a one-shot
// approve, and the card must not offer the trust shortcut at all.
func TestWSApprover_PromptCommand_TrustToolBatchNotCachable(t *testing.T) {
	sendCalled := make(chan struct{}, 1)
	var gotReq approvalRequest
	a := newWSApprover(func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			gotReq = req
		}
		sendCalled <- struct{}{}
		return nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.PromptCommand(danger.ToolBatchClass, "1. `write_file` — `x`", "batch")
	}()

	<-sendCalled

	a.mu.Lock()
	var pendingID string
	for id := range a.pending {
		pendingID = id
		break
	}
	a.mu.Unlock()
	if pendingID == "" {
		t.Fatal("expected a pending request to appear")
	}

	if gotReq.AllowTrust {
		t.Error("approval_request for tool_batch must not advertise AllowTrust")
	}

	a.HandleResponse(pendingID, "trust")

	if err := <-errCh; err != nil {
		t.Errorf("expected nil (one-shot approve), got: %v", err)
	}
	a.mu.Lock()
	cached := a.approveAll[danger.ToolBatchClass]
	a.mu.Unlock()
	if cached {
		t.Error("tool_batch must not be cached in approveAll by a trust response")
	}
}

func TestWSApprover_PromptOperation(t *testing.T) {
	// PromptOperation should call PromptCommand with Risk and Resource
	var capturedCls danger.RiskClass
	var capturedCmd string
	a := newWSApprover(func(v any) error {
		return nil
	})

	// Override PromptCommand behavior by setting trusted class
	a.approveAll[danger.LocalWrite] = true
	_ = capturedCls
	_ = capturedCmd

	op := danger.ToolOperation{
		Name:     "write_file",
		Resource: "/tmp/test.txt",
		Risk:     danger.LocalWrite,
	}

	// With trusted class, should return nil immediately
	err := a.PromptOperation(op)
	if err != nil {
		t.Errorf("expected nil for trusted operation, got: %v", err)
	}
}

func TestWSApprover_PromptOperation_SendError(t *testing.T) {
	a := newWSApprover(func(v any) error {
		return errors.New("send error")
	})

	op := danger.ToolOperation{
		Name:     "read_file",
		Resource: "/etc/passwd",
		Risk:     danger.SystemWrite,
	}

	err := a.PromptOperation(op)
	if err == nil {
		t.Fatal("expected error for send failure in operation")
	}
}

// ── Test Cancel ────────────────────────────────────────────────────────

func TestWSApprover_Cancel_InterruptsPrompt(t *testing.T) {
	sent := make(chan struct{}, 1)
	var acked bool
	sendFn := func(v any) error {
		if _, ok := v.(approvalRequest); ok {
			select {
			case sent <- struct{}{}:
			default:
			}
			return nil
		}
		if m, ok := v.(map[string]any); ok && m["type"] == "approval_ack" {
			acked = true
		}
		return nil
	}
	a := newWSApprover(sendFn)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(danger.Safe, "test", "")
	}()

	// Wait until the approval request is actually out before cancelling —
	// Cancel interrupts waiters that are already waiting (the production
	// shape: the cancel lands while the approval card is up).
	<-sent
	a.Cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from cancelled PromptCommand")
		}
		if !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("expected 'cancelled' in error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after Cancel() within 3s")
	}

	// Ack event should NOT be sent on cancel — the user didn't respond.
	if acked {
		t.Error("approval_ack should not be sent on cancel")
	}
}

func TestWSApprover_Cancel_Idempotent(t *testing.T) {
	a := newWSApprover(func(v any) error { return nil })
	a.Cancel()
	a.Cancel() // second call should not panic
}

// TestWSApprover_CancelRearmsForLaterPrompts pins the re-arm semantics the
// serve cancel paths depend on: a mid-run cancel must interrupt the CURRENT
// approval wait without poisoning approvals for later prompts on the same
// connection (a permanently closed cancel channel would auto-deny every
// future PromptCommand).
func TestWSApprover_CancelRearmsForLaterPrompts(t *testing.T) {
	sent := make(chan struct{}, 1)
	a := newWSApprover(func(v any) error {
		if _, ok := v.(approvalRequest); ok {
			select {
			case sent <- struct{}{}:
			default:
			}
		}
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.Safe, "first", "") }()
	<-sent // wait until this waiter is live, then interrupt it
	a.Cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("expected cancellation error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after Cancel()")
	}

	// A later prompt on the same approver must still reach a normal approve.
	if _, err := promptAndCaptureRequest(t, a, danger.Safe, "approve"); err != nil {
		t.Errorf("PromptCommand after Cancel = %v, want nil (approver must re-arm)", err)
	}
}

// promptAndCaptureRequest runs PromptCommand against a fake sendFn that
// captures the outbound approvalRequest, replies with the given action,
// and returns both the captured request and any PromptCommand error.
func promptAndCaptureRequest(t *testing.T, a *wsApprover, cls danger.RiskClass, action string) (approvalRequest, error) {
	t.Helper()
	captured := make(chan approvalRequest, 1)
	a.sendFn = func(v any) error {
		if req, ok := v.(approvalRequest); ok {
			captured <- req
		}
		return nil
	}
	errCh := make(chan error, 1)
	go func() { errCh <- a.PromptCommand(cls, "cmd", "test") }()

	req := <-captured
	a.HandleResponse(req.ID, action)
	return req, <-errCh
}

// TestWSApprover_AllowTrustFlag_PerClass verifies the outbound approval
// request advertises AllowTrust=false only for destructive and blocked.
func TestWSApprover_AllowTrustFlag_PerClass(t *testing.T) {
	cases := []struct {
		cls       danger.RiskClass
		wantAllow bool
	}{
		{danger.Safe, true},
		{danger.LocalWrite, true},
		{danger.SystemWrite, true},
		{danger.NetworkEgress, true},
		{danger.CodeExecution, true},
		{danger.Install, true},
		{danger.Destructive, false},
		{danger.Blocked, false},
		{danger.Unknown, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.cls), func(t *testing.T) {
			a := newWSApprover(nil)
			req, err := promptAndCaptureRequest(t, a, tc.cls, "approve")
			if err != nil {
				t.Fatalf("PromptCommand: %v", err)
			}
			if req.AllowTrust != tc.wantAllow {
				t.Errorf("AllowTrust = %v, want %v for %s", req.AllowTrust, tc.wantAllow, tc.cls)
			}
		})
	}
}

// TestWSApprover_TrustResponse_CoercedToApprove_ForDestructive verifies
// that even if a forged or stale UI sends action="trust" for a
// destructive prompt, the server treats it as a single approve and does
// NOT cache the class as trusted.
func TestWSApprover_TrustResponse_CoercedToApprove_ForDestructive(t *testing.T) {
	a := newWSApprover(nil)
	_, err := promptAndCaptureRequest(t, a, danger.Destructive, "trust")
	if err != nil {
		t.Errorf("expected nil error (coerced to approve), got: %v", err)
	}
	if a.approveAll[danger.Destructive] {
		t.Error("destructive class was cached as trusted — class trust must be impossible")
	}
}

// TestWSApprover_FrictionTriggersAfterThreshold checks that recording
// FrictionThreshold approvals of the same class causes shouldFriction
// to return true.
func TestWSApprover_FrictionTriggersAfterThreshold(t *testing.T) {
	a := newWSApprover(nil)
	a.frictionThreshold = 3
	for i := 0; i < 3; i++ {
		if friction, _ := a.shouldFriction(danger.SystemWrite); friction {
			t.Errorf("friction true before threshold (i=%d)", i)
		}
		a.recordApproval(danger.SystemWrite)
	}
	friction, count := a.shouldFriction(danger.SystemWrite)
	if !friction {
		t.Error("friction false after threshold reached")
	}
	if count != 3 {
		t.Errorf("FrictionApprovals = %d, want 3", count)
	}
}

// TestWSApprover_FrictionFlagInRequest verifies that once friction is
// active, the outbound approvalRequest carries Friction=true and the
// recent approval count.
func TestWSApprover_FrictionFlagInRequest(t *testing.T) {
	a := newWSApprover(nil)
	a.frictionThreshold = 2
	// Pre-load 2 approvals so the next prompt is in friction mode.
	a.recordApproval(danger.SystemWrite)
	a.recordApproval(danger.SystemWrite)

	req, err := promptAndCaptureRequest(t, a, danger.SystemWrite, "approve")
	if err != nil {
		t.Fatalf("PromptCommand: %v", err)
	}
	if !req.Friction {
		t.Error("expected Friction=true in request after threshold")
	}
	if req.FrictionApprovals != 2 {
		t.Errorf("FrictionApprovals = %d, want 2", req.FrictionApprovals)
	}
}

// TestWSApprover_TrustResponse_CoercedToApprove_ForBlocked is the
// matching case for the Blocked class.
func TestWSApprover_TrustResponse_CoercedToApprove_ForBlocked(t *testing.T) {
	a := newWSApprover(nil)
	_, err := promptAndCaptureRequest(t, a, danger.Blocked, "trust")
	if err != nil {
		t.Errorf("expected nil error (coerced to approve), got: %v", err)
	}
	if a.approveAll[danger.Blocked] {
		t.Error("blocked class was cached as trusted — class trust must be impossible")
	}
}

// TestWSApprover_TrustResponse_CoercedToApprove_ForUnknown verifies the
// fail-closed Unknown class cannot be class-trusted: a forged "trust" is
// treated as a single approve and never cached, so unrecognised verbs can't
// be blanket-approved by one social-engineered grant.
func TestWSApprover_TrustResponse_CoercedToApprove_ForUnknown(t *testing.T) {
	a := newWSApprover(nil)
	_, err := promptAndCaptureRequest(t, a, danger.Unknown, "trust")
	if err != nil {
		t.Errorf("expected nil error (coerced to approve), got: %v", err)
	}
	if a.approveAll[danger.Unknown] {
		t.Error("unknown class was cached as trusted — class trust must be impossible")
	}
}

// TestWSApprover_RequestCarriesEffectiveTimeoutSeconds verifies the outbound
// approval_request frame advertises timeout_seconds — the EFFECTIVE wait the
// server enforces (60s default; SetApprovalTimeout override) — so the browser
// can render a live countdown and autoclose expired cards. The assertion runs
// on the JSON wire form, pinning the snake_case tag and the populated value.
func TestWSApprover_RequestCarriesEffectiveTimeoutSeconds(t *testing.T) {
	assertTimeout := func(t *testing.T, a *wsApprover, want int) {
		t.Helper()
		req, err := promptAndCaptureRequest(t, a, danger.Safe, "approve")
		if err != nil {
			t.Fatalf("PromptCommand: %v", err)
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal approvalRequest: %v", err)
		}
		var wire map[string]any
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatalf("unmarshal approval_request wire JSON: %v", err)
		}
		got, ok := wire["timeout_seconds"].(float64)
		if !ok {
			t.Fatalf("approval_request frame missing timeout_seconds, wire JSON: %s", b)
		}
		if int(got) != want {
			t.Errorf("timeout_seconds = %d, want %d (must match the server-enforced wait)", int(got), want)
		}
	}

	// Default socket UX timeout.
	assertTimeout(t, newWSApprover(nil), 60)

	// Headless runs raise the wait via SetApprovalTimeout — the frame must
	// advertise the same value the enforcer will actually use.
	configured := newWSApprover(nil)
	configured.SetApprovalTimeout(120 * time.Second)
	assertTimeout(t, configured, 120)
}

// TestWSApprover_TimeoutEmitsExpiredFrame verifies that when the approval
// wait expires, the server emits an approval_expired frame carrying the
// request id BEFORE the timeout error surfaces to the run. Without it the
// browser keeps a zombie approval card that blocks the whole approval queue.
// Also pins that a late response for the expired id is dropped safely.
func TestWSApprover_TimeoutEmitsExpiredFrame(t *testing.T) {
	var mu sync.Mutex
	var reqID string
	expired := make(chan string, 4)
	sent := make(chan struct{}, 1)
	a := newWSApprover(func(v any) error {
		if m, ok := v.(map[string]any); ok && m["type"] == "approval_expired" {
			id, _ := m["id"].(string)
			expired <- id
			return nil
		}
		if req, ok := v.(approvalRequest); ok {
			mu.Lock()
			reqID = req.ID
			mu.Unlock()
			select {
			case sent <- struct{}{}:
			default:
			}
		}
		return nil
	})
	a.SetApprovalTimeout(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- a.PromptCommand(danger.Safe, "slow", "test") }()
	<-sent // approval card is up

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("expected timeout error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptCommand did not return after the approval timeout")
	}

	// sendFn is synchronous and the expired frame is emitted inside the
	// timeout branch before the error returns, so the frame must already
	// be out by the time the run observes the error.
	mu.Lock()
	wantID := reqID
	mu.Unlock()
	select {
	case gotID := <-expired:
		if gotID != wantID {
			t.Errorf("approval_expired id = %q, want %q", gotID, wantID)
		}
	default:
		t.Fatal("no approval_expired frame emitted on approval timeout")
	}

	// Late responses for the expired id must keep being dropped: the
	// pending entry is gone, so nothing can satisfy a stale card.
	if a.HandleResponse(wantID, "approve") {
		t.Error("late approval_response for expired id matched a pending request")
	}
}
