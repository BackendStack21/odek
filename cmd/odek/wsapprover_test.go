package main

import (
	"errors"
	"strings"
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
	var acked bool
	sendFn := func(v any) error {
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

	// Cancel immediately.
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
