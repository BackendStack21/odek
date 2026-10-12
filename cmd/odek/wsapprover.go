package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/session"
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

	// TimeoutSeconds is the effective server-enforced wait for this
	// approval, in whole seconds (60 by default; raised for headless REST
	// runs via SetApprovalTimeout). The UI renders its countdown from it
	// and autocloses the card when the matching approval_expired frame
	// arrives. omitempty keeps pre-timeout clients unaffected.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
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

// clarifyRequest is sent to the browser when the agent asks a question.
type clarifyRequest struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Question       string `json:"question"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// clarifyResponse is received from the browser with the principal's answer.
type clarifyResponse struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Answer string `json:"answer"`
	Action string `json:"action,omitempty"`
}

// wsApprover implements danger.Approver over a WebSocket channel.
// It blocks the agent loop until the browser user responds to the
// approval prompt. The caller must call HandleResponse() when a
// matching response arrives from the WebSocket.
type wsApprover struct {
	sendFn         func(v any) error      // sends JSON to WebSocket
	pending        map[string]chan string // request ID → response channel
	decisions      []session.Decision
	pendingClarify map[string]chan string // clarify ID → answer channel
	mu             sync.Mutex
	approveAll     map[danger.RiskClass]bool // trust-cached risk classes
	trustAll       bool                      // when true, all PromptCommand calls auto-approve

	// cancel is closed by Cancel() to interrupt waiting PromptCommand calls.
	// Cancel() closes the active channel and installs a fresh one, so a
	// mid-connection cancel (user aborts a run from the Web UI) breaks the
	// CURRENT approval wait without poisoning approvals for later prompts
	// on the same connection — a permanently closed channel would auto-deny
	// every future approval. cancelMu guards swap+close; PromptCommand reads
	// the active channel via cancelChan.
	cancelMu sync.Mutex
	cancel   chan struct{}
	// closed is set by Close for a one-shot owner (a headless REST run)
	// that will never answer another prompt: the current generation is
	// closed and not re-armed, and every later prompt fails immediately.
	closed bool

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
		pendingClarify:    make(map[string]chan string),
		approveAll:        make(map[danger.RiskClass]bool),
		cancel:            make(chan struct{}),
		frictionThreshold: 3,
		frictionWindow:    60 * time.Second,
		approvalLog:       make(map[danger.RiskClass][]time.Time),
		approvalTimeout:   60 * time.Second,
	}
}

// isClosed reports whether Close was called.
func (a *wsApprover) isClosed() bool {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	return a.closed
}

// cancelChan returns the channel closed by the current Cancel generation.
func (a *wsApprover) cancelChan() <-chan struct{} {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	return a.cancel
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
	// A closed approver belongs to a run that already ended: nobody can
	// answer, so deny at once (even trusted classes) instead of waiting
	// out the approval timeout on a request no client can see.
	if a.isClosed() {
		return fmt.Errorf("approval cancelled: %s", danger.SanitizeInline(cmd))
	}
	// Read the trust caches under the lock: PromptCommand runs concurrently
	// across parallel tool calls, while HandleResponse's "trust" path and
	// SetTrustAll write these same fields. An unsynchronised read here against
	// the map write below (or SetTrustAll) is a data race that can fatally
	// crash serve with "concurrent map read and map write".
	friction, approvalCount := a.shouldFriction(cls)
	a.mu.Lock()
	// Trust-all honors the same class gate as the per-class trust shortcut:
	// destructive/blocked/unknown prompts always need a real user decision,
	// even after a blanket trust grant from an earlier benign batch.
	// Friction overrides every cached grant: a class that is being approved
	// at a fatigue-inducing rate must get a deliberate decision each time.
	trusted := a.approveAll[cls] || (a.trustAll && danger.TrustShortcutAllowed(cls))
	a.mu.Unlock()
	if trusted && !friction {
		return nil
	}

	id := a.newID()
	// The UI renders these fields as text, so the human must see what the
	// bytes are: control characters and bidi/invisible format characters
	// arrive as visible escapes, never raw.
	shownCmd := danger.SanitizeForDisplay(cmd)
	shownDescription := danger.SanitizeForDisplay(description)
	decision := session.Decision{ID: id, Kind: "approval", Command: shownCmd, Risk: string(cls), State: "interrupted"}
	defer func() { a.recordDecision(decision) }()
	resp := make(chan string, 1)

	a.mu.Lock()
	a.pending[id] = resp
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	// While friction is engaged the trust shortcut is withheld, and a "trust"
	// response is treated as a plain one-shot approve below.
	allowTrust := allowTrustForClass(cls) && !friction

	// Capture THIS waiter's cancel channel before sending the request. A
	// Cancel landing between sendFn returning and the select below would
	// otherwise swap generations, the select would pick up the fresh (open)
	// channel, and this waiter would miss its wakeup — blocking out the full
	// approval timeout on a run that was already cancelled.
	cancelCh := a.cancelChan()

	// Effective wait, resolved BEFORE the frame goes out: approvalTimeout
	// is the single source of truth for both the wire-advertised countdown
	// and the enforcement deadline, so the client countdown can never
	// disagree with what the server actually enforces.
	a.mu.Lock()
	timeout := a.approvalTimeout
	a.mu.Unlock()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	// Send approval request via WebSocket
	err := a.sendFn(approvalRequest{
		Type:              "approval_request",
		ID:                id,
		Risk:              string(cls),
		Command:           shownCmd,
		Description:       shownDescription,
		IsOperation:       false,
		AllowTrust:        allowTrust,
		Friction:          friction,
		FrictionApprovals: approvalCount,
		TimeoutSeconds:    int(timeout / time.Second),
	})
	if err != nil {
		return fmt.Errorf("approval: send failed: %w", err)
	}

	// Wait for response, cancellation, or the approval timeout.
	select {
	case action := <-resp:
		// Approve-vs-cancel race: both cases can be ready and select picks
		// randomly. Before honoring the action, check whether a cancel
		// landed — against cancelCh, the captured generation, NOT the live
		// a.cancelChan(): every Cancel since our capture closed cancelCh,
		// while the live accessor returns the newer (open) generation and
		// would silently wave the approve through.
		select {
		case <-cancelCh:
			return fmt.Errorf("approval cancelled: %s", danger.SanitizeInline(cmd))
		default:
		}
		if action == "trust" && !allowTrust {
			action = "approve"
		}
		if action != "approve" && action != "trust" {
			action = "deny"
		}
		decision.Action = action
		decision.State = "accepted"
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
			// Trust grants count toward approval friction, same as the TTY
			// path — otherwise reflexive trust-tapping never trips the
			// fatigue guard on the WebUI.
			a.recordApproval(cls)
			return nil
		default:
			return fmt.Errorf("operation denied by user: %s", danger.SanitizeInline(cmd))
		}
	case <-cancelCh:
		// Retire the entry first so a late answer reports not-found.
		a.retirePending(id, resp)
		return fmt.Errorf("approval cancelled: %s", danger.SanitizeInline(cmd))
	case <-time.After(timeout):
		// Retire the entry before announcing the expiry: an answer arriving
		// after this point finds nothing (REST reports not-found) instead of
		// being acknowledged for a command that is denied. An answer that
		// landed in the same instant is still honored.
		if action, ok := a.retirePending(id, resp); ok {
			if action == "approve" || action == "trust" {
				if action == "trust" && !allowTrust {
					action = "approve"
				}
				decision.Action = action
				decision.State = "accepted"
				a.sendFn(map[string]any{"type": "approval_ack", "id": id, "action": action})
				if action == "approve" {
					a.recordApproval(cls)
					return nil
				}
				a.mu.Lock()
				a.approveAll[cls] = true
				a.mu.Unlock()
				a.recordApproval(cls)
				return nil
			}
		}
		decision.State = "expired"
		// Tell the browser this card is dead BEFORE the timeout error
		// surfaces to the run — otherwise the UI keeps a zombie approval
		// card that blocks the whole approval queue.
		a.sendFn(map[string]any{
			"type": "approval_expired",
			"id":   id,
		})
		return fmt.Errorf("approval timeout: %s", danger.SanitizeInline(cmd))
	}
}

func (a *wsApprover) PromptOperation(op danger.ToolOperation) error {
	return a.PromptCommand(op.Risk, op.Resource, fmt.Sprintf("tool=%s", op.Name))
}

// retirePending removes a pending approval under the lock and returns an
// answer that was already delivered but not yet consumed, if any. After it
// returns, HandleResponse for the id reports false.
func (a *wsApprover) retirePending(id string, resp chan string) (string, bool) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
	select {
	case v := <-resp:
		return v, true
	default:
		return "", false
	}
}

// HandleResponse processes a response from the browser.
// Called by the WebSocket read loop when an approval_response arrives.
// Returns true only when the response was delivered to a waiting request.
//
// Delivery is one-shot: the pending entry is removed under the lock as the
// answer is handed over, so a duplicate or late answer for the same id finds
// nothing and reports false instead of being silently dropped yet
// acknowledged. The send is non-blocking and the channel has capacity 1, so
// it can never wedge the WebSocket read goroutine.
func (a *wsApprover) HandleResponse(id, action string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	resp, ok := a.pending[id]
	if !ok {
		return false
	}
	select {
	case resp <- action:
		delete(a.pending, id)
		return true
	default:
		return false
	}
}

func (a *wsApprover) newID() string {
	return a.newPrefixedID("apr-")
}

func (a *wsApprover) newClarifyID() string {
	return a.newPrefixedID("clr-")
}

func (a *wsApprover) newPrefixedID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fail closed: on entropy failure every request would share the
		// zero ID, letting one response satisfy another pending request.
		panic("odek: crypto/rand unavailable for request ID: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

// PromptClarify sends a principal-channel question to the bound WebSocket
// session and blocks until the user answers, the wait expires, or the run
// is cancelled. A failed send returns immediately so the loop can continue.
func (a *wsApprover) PromptClarify(question string) (string, error) {
	if a.isClosed() {
		return "", fmt.Errorf("cancelled")
	}
	id := a.newClarifyID()
	decision := session.Decision{ID: id, Kind: "question", Command: question, State: "interrupted"}
	defer func() { a.recordDecision(decision) }()
	resp := make(chan string, 1)

	a.mu.Lock()
	if a.pendingClarify == nil {
		a.pendingClarify = make(map[string]chan string)
	}
	a.pendingClarify[id] = resp
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.pendingClarify, id)
		a.mu.Unlock()
	}()

	cancelCh := a.cancelChan()
	timeout := clarifyTimeout

	err := a.sendFn(clarifyRequest{
		Type:           "clarify_request",
		ID:             id,
		Question:       question,
		TimeoutSeconds: int(timeout / time.Second),
	})
	if err != nil {
		return "", fmt.Errorf("send failed: %w", err)
	}

	select {
	case answer := <-resp:
		select {
		case <-cancelCh:
			return "", fmt.Errorf("cancelled")
		default:
		}
		if answer == clarifySkip {
			decision.Action = "skip"
			decision.State = "accepted"
			a.sendFn(map[string]any{"type": "clarify_ack", "id": id, "action": "skip"})
			return "", fmt.Errorf("question skipped by user; do not assume an answer")
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return "", fmt.Errorf("empty answer")
		}
		decision.Action = "answer"
		decision.State = "accepted"
		a.sendFn(map[string]any{
			"action": "answer",
			"type":   "clarify_ack",
			"id":     id,
		})
		return answer, nil
	case <-cancelCh:
		a.mu.Lock()
		delete(a.pendingClarify, id)
		a.mu.Unlock()
		return "", fmt.Errorf("cancelled")
	case <-time.After(timeout):
		a.mu.Lock()
		delete(a.pendingClarify, id)
		a.mu.Unlock()
		decision.State = "expired"
		a.sendFn(map[string]any{
			"type": "clarify_expired",
			"id":   id,
		})
		return "", fmt.Errorf("timed out waiting for response")
	}
}

// HandleClarifyResponse delivers a browser answer to a pending PromptClarify.
// Empty answers are ignored so a blank card cannot complete the wait.
func (a *wsApprover) HandleClarifyResponse(id, answer string) bool {
	if len(answer) > 32000 || strings.TrimSpace(answer) == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resp, ok := a.pendingClarify[id]
	if !ok {
		return false
	}
	select {
	case resp <- answer:
		delete(a.pendingClarify, id) // one-shot: a duplicate or late answer finds nothing
		return true
	default:
		return false
	}
}

// Cancel interrupts any pending PromptCommand by closing the active cancel
// channel, then re-arms a fresh one so later prompts on the same connection
// can still ask for approvals (a one-shot close would auto-deny every future
// approval after a mid-run cancel — see the cancel field comment). Safe to
// call multiple times, including concurrently.
func (a *wsApprover) Cancel() {
	a.cancelMu.Lock()
	if a.closed {
		a.cancelMu.Unlock()
		return
	}
	ch := a.cancel
	a.cancel = make(chan struct{})
	a.cancelMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// Close interrupts any pending prompt like Cancel but does not re-arm:
// every later PromptCommand or PromptClarify fails immediately. Used when
// the approver's owner is finished for good (a cancelled headless run),
// where a fresh generation would let a late prompt from the draining loop
// block for the full approval timeout with nobody able to answer it. Safe
// to call multiple times and together with Cancel.
func (a *wsApprover) Close() {
	a.cancelMu.Lock()
	if a.closed {
		a.cancelMu.Unlock()
		return
	}
	a.closed = true
	ch := a.cancel
	a.cancelMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// SetTrustAll enables or disables blanket trust for all risk classes.
func (a *wsApprover) SetTrustAll(enabled bool) {
	a.mu.Lock()
	a.trustAll = enabled
	a.mu.Unlock()
}

// TrustClasses returns a snapshot of explicit grants on this connection.
func (a *wsApprover) TrustClasses() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	for cls, allowed := range a.approveAll {
		if allowed {
			out = append(out, string(cls))
		}
	}
	if a.trustAll {
		out = append(out, "all eligible classes")
	}
	sort.Strings(out)
	return out
}
func (a *wsApprover) RevokeTrust() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approveAll = map[danger.RiskClass]bool{}
	a.trustAll = false
}

// This sentinel never crosses the principal-channel boundary as an answer.
const clarifySkip = "\x00odek-question-skipped\x00"

func (a *wsApprover) HandleClarifySkip(id string) bool {
	return a.HandleClarifyResponse(id, clarifySkip)
}
func (a *wsApprover) recordDecision(item session.Decision) {
	item.At = time.Now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decisions = append(a.decisions, item)
	if len(a.decisions) > 128 {
		a.decisions = a.decisions[len(a.decisions)-128:]
	}
}
func (a *wsApprover) drainDecisions(turnID string) []session.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.decisions
	a.decisions = nil
	for i := range out {
		out[i].TurnID = turnID
	}
	return out
}

type serveApproverKey struct{}
