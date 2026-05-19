package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/BackendStack21/kode/internal/danger"
)

// approvalRequest is sent from the serve WebSocket to the browser
// when the agent needs user approval for a dangerous operation.
type approvalRequest struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Risk        string `json:"risk"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	IsOperation bool   `json:"is_operation"`
}

// approvalResponse is received from the browser when the user responds.
type approvalResponse struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Action string `json:"action"` // "approve", "deny", "trust"
}

// wsApprover implements danger.Approver over a WebSocket channel.
// It blocks the agent loop until the browser user responds to the
// approval prompt. The caller must call HandleResponse() when a
// matching response arrives from the WebSocket.
type wsApprover struct {
	sendFn     func(v any) error            // sends JSON to WebSocket
	pending    map[string]chan string        // request ID → response channel
	mu         sync.Mutex
	approveAll map[danger.RiskClass]bool     // trust-cached risk classes
}

// newWSApprover creates a wsApprover that sends requests via sendFn.
func newWSApprover(sendFn func(v any) error) *wsApprover {
	return &wsApprover{
		sendFn:     sendFn,
		pending:    make(map[string]chan string),
		approveAll: make(map[danger.RiskClass]bool),
	}
}

func (a *wsApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	if a.approveAll[cls] {
		return nil
	}

	id := a.newID()
	resp := make(chan string, 1)

	a.mu.Lock()
	a.pending[id] = resp
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	// Send approval request via WebSocket
	err := a.sendFn(approvalRequest{
		Type:        "approval_request",
		ID:          id,
		Risk:        string(cls),
		Command:     cmd,
		Description: description,
		IsOperation: false,
	})
	if err != nil {
		return fmt.Errorf("approval: send failed: %w", err)
	}

	// Wait for response (60s timeout prevents agent deadlock)
	select {
	case action := <-resp:
		switch action {
		case "approve":
			return nil
		case "trust":
			a.approveAll[cls] = true
			return nil
		default:
			return fmt.Errorf("operation denied by user: %s", cmd)
		}
	case <-time.After(60 * time.Second):
		return fmt.Errorf("approval timeout: %s", cmd)
	}
}

func (a *wsApprover) PromptOperation(op danger.ToolOperation) error {
	return a.PromptCommand(op.Risk, op.Resource, fmt.Sprintf("tool=%s", op.Name))
}

// HandleResponse processes a response from the browser.
// Called by the WebSocket read loop when an approval_response arrives.
// Returns true if the response matched a pending request.
func (a *wsApprover) HandleResponse(id, action string) bool {
	a.mu.Lock()
	resp, ok := a.pending[id]
	a.mu.Unlock()
	if ok {
		resp <- action
	}
	return ok
}

func (a *wsApprover) newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "apr-" + hex.EncodeToString(b)
}
