package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// approvalRequest is sent from the serve WebSocket to the browser
// when the agent needs user approval for a dangerous operation.
//
// AllowTrust reports whether the "trust class for session" shortcut is
// available for this prompt. It is false for the two highest-impact
// classes (destructive, blocked) — those always require a per-call
// approval to defeat approval-fatigue attacks. The UI must hide the
// "Trust" button when AllowTrust is false.
//
// Friction is true when the user has already approved >=
// FrictionThreshold operations of this class inside FrictionWindow.
// In friction mode the UI must show the recent-approval count to the
// user and require typing the literal word "approve" (no button
// click) before forwarding "approve" to the server.
type approvalRequest struct {
	Type              string `json:"type"`
	ID                string `json:"id"`
	Risk              string `json:"risk"`
	Command           string `json:"command"`
	Description       string `json:"description,omitempty"`
	IsOperation       bool   `json:"is_operation"`
	AllowTrust        bool   `json:"allow_trust"`
	Friction          bool   `json:"friction"`
	FrictionApprovals int    `json:"friction_approvals,omitempty"`
}

// allowTrustForClass mirrors the TTYApprover policy: destructive, blocked,
// unknown, and the synthetic tool_batch class must never be class-trusted,
// so an attacker cannot social-engineer a single broad approval into
// long-term carte blanche. Unknown is the fail-closed catch-all for
// unrecognised verbs; class-trusting it would blanket-approve every future
// obfuscated/novel command. tool_batch aggregates multiple tools of
// differing classes in one card — trusting it would silently approve every
// later batch (and, via the loop's SetTrustAll, every per-tool prompt).
func allowTrustForClass(cls danger.RiskClass) bool {
	return danger.TrustShortcutAllowed(cls)
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
	sendFn     func(v any) error      // sends JSON to WebSocket
	pending    map[string]chan string // request ID → response channel
	mu         sync.Mutex
	approveAll map[danger.RiskClass]bool // trust-cached risk classes
	trustAll   bool                      // when true, all PromptCommand calls auto-approve
	cancel     chan struct{}             // closed by Cancel() to interrupt waiting PromptCommand

	// cancelOnce guards channel closure. The select/default close idiom is
	// racy: two concurrent Cancel calls can both observe an open channel and
	// the second close panics — reachable in serve when the WS reader errors
	// while teardown's deferred Cancel runs.
	cancelOnce sync.Once

	// Approval-fatigue mitigation. Parallel to the TTYApprover policy.
	frictionThreshold int
	frictionWindow    time.Duration
	approvalLog       map[danger.RiskClass][]time.Time

	// approvalTimeout bounds how long PromptCommand waits for a response.
	// Defaults to 60s (the socket UX); headless REST runs may raise it via
	// SetApprovalTimeout (capped by the caller) for poll-based clients.
	approvalTimeout time.Duration
}

// newWSApprover creates a wsApprover that sends requests via sendFn.
func newWSApprover(sendFn func(v any) error) *wsApprover {
	return &wsApprover{
		sendFn:            sendFn,
		pending:           make(map[string]chan string),
		approveAll:        make(map[danger.RiskClass]bool),
		cancel:            make(chan struct{}),
		frictionThreshold: 3,
		frictionWindow:    60 * time.Second,
		approvalLog:       make(map[danger.RiskClass][]time.Time),
		approvalTimeout:   60 * time.Second,
	}
}

// SetApprovalTimeout adjusts how long PromptCommand waits for a response.
// Intended for headless REST runs whose operator polls at a slower cadence
// than a browser; callers cap the value themselves.
func (a *wsApprover) SetApprovalTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	a.mu.Lock()
	a.approvalTimeout = d
	a.mu.Unlock()
}

// recordApproval logs an approval timestamp for cls.
func (a *wsApprover) recordApproval(cls danger.RiskClass) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approvalLog == nil {
		a.approvalLog = make(map[danger.RiskClass][]time.Time)
	}
	a.approvalLog[cls] = append(a.approvalLog[cls], time.Now())
}

// shouldFriction returns true and the recent approval count when the
// next prompt for cls should engage the high-friction UI path.
func (a *wsApprover) shouldFriction(cls danger.RiskClass) (bool, int) {
	if a.frictionThreshold <= 0 || a.frictionWindow <= 0 {
		return false, 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-a.frictionWindow)
	log := a.approvalLog[cls]
	kept := log[:0]
	for _, t := range log {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	a.approvalLog[cls] = kept
	return len(kept) >= a.frictionThreshold, len(kept)
}

func (a *wsApprover) PromptCommand(cls danger.RiskClass, cmd, description string) error {
	// Read the trust caches under the lock: PromptCommand runs concurrently
	// across parallel tool calls, while HandleResponse's "trust" path and
	// SetTrustAll write these same fields. An unsynchronised read here against
	// the map write below (or SetTrustAll) is a data race that can fatally
	// crash serve with "concurrent map read and map write".
	a.mu.Lock()
	trusted := a.approveAll[cls] || a.trustAll
	a.mu.Unlock()
	if trusted {
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

	allowTrust := allowTrustForClass(cls)
	friction, approvalCount := a.shouldFriction(cls)

	// Send approval request via WebSocket
	err := a.sendFn(approvalRequest{
		Type:              "approval_request",
		ID:                id,
		Risk:              string(cls),
		Command:           cmd,
		Description:       description,
		IsOperation:       false,
		AllowTrust:        allowTrust,
		Friction:          friction,
		FrictionApprovals: approvalCount,
	})
	if err != nil {
		return fmt.Errorf("approval: send failed: %w", err)
	}

	// Wait for response, cancellation, or the approval timeout.
	a.mu.Lock()
	timeout := a.approvalTimeout
	a.mu.Unlock()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	select {
	case action := <-resp:
		// Ack the user's choice back to the browser for UI feedback.
		a.sendFn(map[string]any{
			"type":   "approval_ack",
			"id":     id,
			"action": action,
		})
		switch action {
		case "approve":
			a.recordApproval(cls)
			return nil
		case "trust":
			if !allowTrust {
				// A forged or stale UI may still send "trust" for a
				// destructive/blocked prompt. Coerce to a one-shot
				// approve so the single operation runs but no class-
				// wide trust is cached.
				a.recordApproval(cls)
				return nil
			}
			a.mu.Lock()
			a.approveAll[cls] = true
			a.mu.Unlock()
			return nil
		default:
			return fmt.Errorf("operation denied by user: %s", cmd)
		}
	case <-a.cancel:
		return fmt.Errorf("approval cancelled: %s", cmd)
	case <-time.After(timeout):
		return fmt.Errorf("approval timeout: %s", cmd)
	}
}

func (a *wsApprover) PromptOperation(op danger.ToolOperation) error {
	return a.PromptCommand(op.Risk, op.Resource, fmt.Sprintf("tool=%s", op.Name))
}

// HandleResponse processes a response from the browser.
// Called by the WebSocket read loop when an approval_response arrives.
// Returns true if the response matched a pending request.
//
// The send to the response channel is non-blocking: the channel has capacity
// 1, so if a duplicate or late response races with the first one, or if the
// request has already timed out and the reader is gone, the send drops the
// response instead of wedging the WebSocket read goroutine.
func (a *wsApprover) HandleResponse(id, action string) bool {
	a.mu.Lock()
	resp, ok := a.pending[id]
	a.mu.Unlock()
	if ok {
		select {
		case resp <- action:
		default:
		}
	}
	return ok
}

func (a *wsApprover) newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fail closed: on entropy failure every approval would share the
		// zero ID, letting one response satisfy another pending request.
		panic("odek: crypto/rand unavailable for approval ID: " + err.Error())
	}
	return "apr-" + hex.EncodeToString(b)
}

// Cancel interrupts any pending PromptCommand by closing the cancel channel.
// Safe to call multiple times, including concurrently.
func (a *wsApprover) Cancel() {
	a.cancelOnce.Do(func() { close(a.cancel) })
}

// SetTrustAll enables or disables blanket trust for all risk classes.
func (a *wsApprover) SetTrustAll(enabled bool) {
	a.mu.Lock()
	a.trustAll = enabled
	a.mu.Unlock()
}
