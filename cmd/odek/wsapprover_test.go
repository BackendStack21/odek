package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/danger"
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
