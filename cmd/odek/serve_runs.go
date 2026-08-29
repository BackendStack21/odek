package main

// serve_runs.go — headless REST agent runs + server observability:
//
//	POST   /api/prompt                     start a run (no WebSocket needed)
//	GET    /api/runs                       recent runs
//	GET    /api/runs/{id}                  run detail (status, result, events)
//	DELETE /api/runs/{id}                  cancel a run
//	GET    /api/runs/{id}/approvals        pending approval requests
//	POST   /api/runs/{id}/approvals/{aid}  answer one (approve|deny|trust)
//	POST   /api/runs/{id}/cancel           cancel a run
//	GET    /api/events                     recent odek.event/v1 runtime events
//	GET    /api/usage                      server-lifetime usage aggregates
//	GET    /api/connections                live WebSocket connections
//	DELETE /api/connections/{id}           kick a connection
//
// Runs execute the exact same handlePrompt path as WebSocket prompts
// (@-refs, attachments, audit, per-turn persistence), so a REST client gets
// identical semantics — only the transport differs. Approval prompts block
// inside the agent loop exactly as they do on the socket; the REST approval
// bridge lets a management client answer them remotely through
// wsApprover.HandleResponse (same trust caching and friction as the UI).
//
// Auth: every endpoint sits behind the apiAuth wrapper (per-instance token +
// loopback Host + local-origin on mutations), i.e. operator-only. The agent
// itself can neither read nor answer these endpoints — its browser/http
// tools refuse loopback via the SSRF guard and it never holds the token.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// ── Runtime events ring ───────────────────────────────────────────────

// serveEventsCap bounds the in-memory odek.event/v1 ring. Events already
// carry SHA-256 arg hashes (never raw args) and pass redaction at emission,
// so retaining them is safe; the cap keeps a hostile run from growing the
// buffer without bound.
const serveEventsCap = 500

type eventsRing struct {
	mu     sync.Mutex
	events []events.Event
}

var serveEvents = &eventsRing{}

func (r *eventsRing) add(ev events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	if len(r.events) > serveEventsCap {
		r.events = r.events[len(r.events)-serveEventsCap:]
	}
}

// snapshot returns up to limit most-recent events, filtered by run_id /
// session_id when non-empty, oldest-first. With more matches than the
// limit, the window is the MOST RECENT matches (previously it walked
// oldest-first and stopped at the limit, serving stale events once the
// ring held more entries than the limit). A limit <= 0 returns all
// matching events, oldest-first.
func (r *eventsRing) snapshot(limit int, runID, sessionID string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	matches := r.events
	if runID != "" || sessionID != "" {
		matches = make([]events.Event, 0, len(r.events))
		for _, ev := range r.events {
			if runID != "" && ev.RunID != runID {
				continue
			}
			if sessionID != "" && ev.SessionID != sessionID {
				continue
			}
			matches = append(matches, ev)
		}
	}

	if limit > 0 && len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	out := make([]events.Event, len(matches))
	copy(out, matches) // ring may keep appending; don't alias its slice
	return out
}

func (r *eventsRing) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

// handleEvents serves the recent runtime-event feed (GET /api/events).
func handleEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
		if limit < 1 {
			limit = 100
		}
		if limit > serveEventsCap {
			limit = serveEventsCap
		}
		evs := serveEvents.snapshot(limit, r.URL.Query().Get("run_id"), r.URL.Query().Get("session_id"))
		if evs == nil {
			evs = []events.Event{}
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"events": evs, "count": len(evs)})
	}
}

// ── Server usage aggregates ────────────────────────────────────────────

// serveStats accumulates server-lifetime counters. handlePrompt is the
// single increment site so WebSocket prompts and REST runs are counted
// identically.
var serveStats struct {
	PromptsStarted   int64
	PromptsCompleted int64
	PromptsFailed    int64
	TokensIn         int64
	TokensOut        int64
}

// handleUsage reports server-lifetime usage plus an estimated spend from the
// resolved per-million prices. Costs are absent (0, prices_configured=false)
// when the operator has not configured prices — clients must render that as
// "unavailable", not $0.
func handleUsage(resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		in := atomic.LoadInt64(&serveStats.TokensIn)
		out := atomic.LoadInt64(&serveStats.TokensOut)
		inPrice, outPrice := resolved.Limits.ResolvePrices(resolved.Model)
		cost := float64(in)/1e6*inPrice + float64(out)/1e6*outPrice
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"prompts_started":    atomic.LoadInt64(&serveStats.PromptsStarted),
			"prompts_completed":  atomic.LoadInt64(&serveStats.PromptsCompleted),
			"prompts_failed":     atomic.LoadInt64(&serveStats.PromptsFailed),
			"tokens_in":          in,
			"tokens_out":         out,
			"estimated_cost_usd": cost,
			"prices_configured":  inPrice > 0 || outPrice > 0,
			"model":              resolved.Model,
			"ws_connections":     atomic.LoadInt64(&serveWSConnections),
			"runs_active":        activeRunCount(),
			"subagents":          subagentStatsSnapshot(),
		})
	}
}

// ── WebSocket connection registry ─────────────────────────────────────

// wsConnInfo tracks one live WebSocket connection. The handleWS processor
// loop mutates the live fields under mu; snapshots serialize only the wire
// fields. RemoteAddr is the socket peer; session/model/busy reflect the
// connection's most recent state. No tokens are ever stored here.
type wsConnInfo struct {
	// Wire fields (serialized).
	ID          string    `json:"id"`
	RemoteAddr  string    `json:"remote_addr"`
	ConnectedAt time.Time `json:"connected_at"`
	Prompts     int64     `json:"prompts"`
	SessionID   string    `json:"session_id"`
	Model       string    `json:"model"`
	Busy        bool      `json:"busy"`

	// Live state (never serialized; unexported).
	mu   sync.Mutex
	conn *golangws.Conn
}

func (c *wsConnInfo) setLive(session string, busy bool) {
	c.mu.Lock()
	c.SessionID, c.Busy = session, busy
	c.mu.Unlock()
}

// recordPrompt bumps the per-connection prompt counter under the lock.
func (c *wsConnInfo) recordPrompt() {
	c.mu.Lock()
	c.Prompts++
	c.mu.Unlock()
}

// wireCopy returns a serialization-safe copy. Built field-by-field rather
// than a struct copy — wsConnInfo embeds a sync.Mutex, and copying a locked
// (or even unlocked) lock by value is a vet error and a real bug source.
func (c *wsConnInfo) wireCopy() wsConnInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsConnInfo{
		ID:          c.ID,
		RemoteAddr:  c.RemoteAddr,
		ConnectedAt: c.ConnectedAt,
		Prompts:     c.Prompts,
		SessionID:   c.SessionID,
		Model:       c.Model,
		Busy:        c.Busy,
	}
}

// kick closes the underlying socket. That unblocks the handleWS reader; its
// defers (agent.Close, sandbox cleanup) then run, so a kick is a clean
// teardown, not a leak.
func (c *wsConnInfo) kick() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// wsConnRegistry maps connection IDs to live connection info.
var wsConnRegistry struct {
	mu    sync.RWMutex
	conns map[string]*wsConnInfo
}

func init() { wsConnRegistry.conns = map[string]*wsConnInfo{} }

func wsConnRegister(info *wsConnInfo) {
	wsConnRegistry.mu.Lock()
	wsConnRegistry.conns[info.ID] = info
	wsConnRegistry.mu.Unlock()
}

func wsConnUnregister(id string) {
	wsConnRegistry.mu.Lock()
	delete(wsConnRegistry.conns, id)
	wsConnRegistry.mu.Unlock()
}

func wsConnList() []wsConnInfo {
	wsConnRegistry.mu.RLock()
	defer wsConnRegistry.mu.RUnlock()
	out := make([]wsConnInfo, 0, len(wsConnRegistry.conns))
	for _, c := range wsConnRegistry.conns {
		out = append(out, c.wireCopy())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

func wsConnKick(id string) bool {
	wsConnRegistry.mu.RLock()
	c, ok := wsConnRegistry.conns[id]
	wsConnRegistry.mu.RUnlock()
	if !ok {
		return false
	}
	c.kick()
	return true
}

func newWSConnID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fail closed like session.GenerateAuthToken: on entropy failure a
		// zero ID would be predictable AND collide across connections
		// (kick/cancel would target the first match).
		panic("odek: crypto/rand unavailable for connection ID: " + err.Error())
	}
	return "conn-" + hex.EncodeToString(b)
}

// handleConnections implements GET /api/connections.
func handleConnections() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		conns := wsConnList()
		writeAPIJSON(w, http.StatusOK, map[string]any{"connections": conns, "count": len(conns)})
	}
}

// handleConnectionKick implements DELETE /api/connections/{id}.
func handleConnectionKick() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/connections/")
		if id == "" {
			http.Error(w, "missing connection id", http.StatusBadRequest)
			return
		}
		if !wsConnKick(id) {
			http.Error(w, "connection not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Headless runs ─────────────────────────────────────────────────────

const (
	serveRunsCap           = 100 // total registry bound
	serveRunsCompletedKept = 20  // newest completed runs kept on eviction
	maxRunApprovalWait     = 10 * time.Minute
	serveRunEventsKept     = 200 // per-run event tail bound
	// maxActiveServeRuns caps concurrently-running (or approval-waiting)
	// REST runs. The WS surface caps itself at maxWSConnections (each
	// connection spawns an agent — and a sandbox container); the REST run
	// path spawns the same per-run cost, so it gets the same bound. Without
	// it, a script looping POST /api/prompt could spawn unbounded agents,
	// MCP clients, and containers (2026-08 audit).
	maxActiveServeRuns = 20
)

// Sentinel errors mapped to HTTP statuses by handlePromptStart.
var (
	errInvalidSessionToken = errors.New("invalid session token")
	errTooManyActiveRuns   = errors.New("too many active runs")
)

// serveRun is one headless agent execution started via POST /api/prompt.
type serveRun struct {
	// Serialized state (via snapshot).
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	Model        string    `json:"model"`
	Status       string    `json:"status"` // running | waiting_approval | completed | failed | cancelled
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	Result       string    `json:"result,omitempty"`
	Error        string    `json:"error,omitempty"`

	mu       sync.Mutex
	cond     *sync.Cond
	events   []map[string]any // bounded event tail
	pending  map[string]*approvalRequest
	approver *wsApprover
	cancel   context.CancelFunc
	cleanup  func()
}

// record is the run's event sink — the sendFn handed to newServeAgent and
// handlePrompt instead of a WebSocket. It captures approval requests for
// the REST bridge, accumulates the answer text, and keeps a bounded tail.
func (r *serveRun) record(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch m["type"] {
	case "approval_request":
		if req, ok := approvalRequestFromMap(m); ok {
			r.pending[req.ID] = req
			r.Status = "waiting_approval"
		}
	case "approval_ack":
		if id, _ := m["id"].(string); id != "" {
			delete(r.pending, id)
		}
		if len(r.pending) == 0 && r.Status == "waiting_approval" {
			r.Status = "running"
		}
	case "token", "token_delta":
		if c, _ := m["content"].(string); c != "" {
			r.Result += c
		}
	case "error":
		if msg, _ := m["message"].(string); msg != "" {
			r.Error = msg
		}
	case "done":
		// Numeric fields arrive as Go ints inside the event map (they are
		// only float64 after a JSON round trip) — accept both.
		r.InputTokens = numberOf(m["contextTokens"])
		r.OutputTokens = numberOf(m["outputTokens"])
	case "session":
		// The session event carries the session auth token for live WS
		// clients; recording it into the run's event tail would hand the
		// token to any instance-token holder via GET /api/runs/{id},
		// defeating the §24 cookie-only-vs-token-holder boundary
		// (2026-08 audit). Strip it from the recorded copy.
		delete(m, "auth_token")
	}
	r.events = append(r.events, m)
	if len(r.events) > serveRunEventsKept {
		r.events = r.events[len(r.events)-serveRunEventsKept:]
	}
	r.cond.Broadcast()
	return nil
}

// numberOf extracts an int64 from a map value that may be int, int64, or
// float64 (depending on whether the map came from Go code or JSON).
func numberOf(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// approvalRequestFromMap rebuilds an approvalRequest from the generic event
// map (the wsApprover sends its typed struct through sendFn as a value, so
// the typed path is handled by the caller; this covers the map form).
func approvalRequestFromMap(m map[string]any) (*approvalRequest, bool) {
	id, _ := m["id"].(string)
	if id == "" {
		return nil, false
	}
	req := &approvalRequest{ID: id}
	req.Type, _ = m["type"].(string)
	req.Risk, _ = m["risk"].(string)
	req.Command, _ = m["command"].(string)
	req.Description, _ = m["description"].(string)
	if b, ok := m["is_operation"].(bool); ok {
		req.IsOperation = b
	}
	if b, ok := m["allow_trust"].(bool); ok {
		req.AllowTrust = b
	}
	if b, ok := m["friction"].(bool); ok {
		req.Friction = b
	}
	req.FrictionApprovals = int(numberOf(m["friction_approvals"]))
	return req, true
}

// recordApprovalRequest captures a typed approvalRequest (the form
// wsApprover actually sends).
func (r *serveRun) recordApprovalRequest(req approvalRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := req
	r.pending[req.ID] = &cp
	if r.Status == "running" {
		r.Status = "waiting_approval"
	}
	r.cond.Broadcast()
}

// finish transitions the run to a terminal status (once) and wakes waiters.
func (r *serveRun) finish(status, errMsg string) {
	r.mu.Lock()
	if !runStatusTerminal(r.Status) {
		r.Status = status
	}
	if errMsg != "" && r.Error == "" {
		r.Error = errMsg
	}
	r.EndedAt = time.Now().UTC()
	r.pending = map[string]*approvalRequest{}
	r.cond.Broadcast()
	r.mu.Unlock()
}

func runStatusTerminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled"
}

// snapshot returns the serializable run state, optionally with the event
// tail and pending approvals expanded.
func (r *serveRun) snapshot(includeEvents bool) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{
		"id":            r.ID,
		"session_id":    r.SessionID,
		"model":         r.Model,
		"status":        r.Status,
		"started_at":    r.StartedAt,
		"ended_at":      r.EndedAt,
		"input_tokens":  r.InputTokens,
		"output_tokens": r.OutputTokens,
		"result":        r.Result,
		"error":         r.Error,
	}
	pending := make([]map[string]any, 0, len(r.pending))
	for _, p := range r.pending {
		pending = append(pending, map[string]any{
			"id": p.ID, "risk": p.Risk, "command": p.Command,
			"description": p.Description, "allow_trust": p.AllowTrust,
			"friction": p.Friction, "friction_approvals": p.FrictionApprovals,
		})
	}
	out["pending_approvals"] = pending
	if includeEvents {
		evs := make([]map[string]any, len(r.events))
		copy(evs, r.events)
		out["events"] = evs
	}
	return out
}

func (r *serveRun) isTerminal() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runStatusTerminal(r.Status)
}

func newRunID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		// Fail closed: predictable/colliding run IDs would let one run's
		// cancel/approval operations target another's.
		panic("odek: crypto/rand unavailable for run ID: " + err.Error())
	}
	return "run-" + hex.EncodeToString(b)
}

// ── Run registry ──────────────────────────────────────────────────────

var serveRuns struct {
	mu   sync.Mutex
	runs map[string]*serveRun
}

func init() { serveRuns.runs = map[string]*serveRun{} }

// registerRun adds the run to the registry, evicting the oldest completed
// runs when over the cap. Running runs are never evicted.
func registerRun(r *serveRun) {
	serveRuns.mu.Lock()
	defer serveRuns.mu.Unlock()
	serveRuns.runs[r.ID] = r
	if len(serveRuns.runs) <= serveRunsCap {
		return
	}
	type aged struct {
		id string
		t  time.Time
	}
	var completed []aged
	for id, rr := range serveRuns.runs {
		rr.mu.Lock()
		st, end := rr.Status, rr.EndedAt
		rr.mu.Unlock()
		if runStatusTerminal(st) {
			completed = append(completed, aged{id, end})
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].t.Before(completed[j].t) })
	if len(completed) == 0 {
		// Nothing evictable: every run is still active. The active-run cap
		// bounds that population, so the registry cannot grow without
		// bound here (the old hard-cap clause no-oped in this case —
		// 2026-08 audit).
		return
	}
	// Evict as many of the oldest completed runs as needed to get back to
	// the cap, but keep the newest serveRunsCompletedKept when possible.
	evict := len(serveRuns.runs) - serveRunsCap
	if maxEvict := len(completed) - serveRunsCompletedKept; evict > maxEvict {
		evict = maxEvict
	}
	if evict < 1 {
		evict = 1 // hard cap wins — must shrink somehow
	}
	if evict > len(completed) {
		evict = len(completed)
	}
	for _, a := range completed[:evict] {
		delete(serveRuns.runs, a.id)
	}
}

func lookupRun(id string) *serveRun {
	serveRuns.mu.Lock()
	defer serveRuns.mu.Unlock()
	return serveRuns.runs[id]
}

func activeRunCount() int {
	serveRuns.mu.Lock()
	defer serveRuns.mu.Unlock()
	n := 0
	for _, rr := range serveRuns.runs {
		rr.mu.Lock()
		if rr.Status == "running" || rr.Status == "waiting_approval" {
			n++
		}
		rr.mu.Unlock()
	}
	return n
}

// listRunsWire returns run snapshots (no events) newest-first.
func listRunsWire(limit int) []map[string]any {
	serveRuns.mu.Lock()
	runs := make([]*serveRun, 0, len(serveRuns.runs))
	for _, rr := range serveRuns.runs {
		runs = append(runs, rr)
	}
	serveRuns.mu.Unlock()

	out := make([]map[string]any, 0, len(runs))
	for _, rr := range runs {
		out = append(out, rr.snapshot(false))
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["started_at"].(time.Time)
		b, _ := out[j]["started_at"].(time.Time)
		return a.After(b)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// resetServeRuns clears the registry (tests).
func resetServeRuns() {
	serveRuns.mu.Lock()
	serveRuns.runs = map[string]*serveRun{}
	serveRuns.mu.Unlock()
}

// ── Run lifecycle ─────────────────────────────────────────────────────

// promptRequest is the body of POST /api/prompt.
type promptRequest struct {
	Content                string         `json:"content"`
	SessionID              string         `json:"session_id"`
	AuthToken              string         `json:"auth_token"`
	Model                  string         `json:"model"`
	Thinking               string         `json:"thinking"`
	ApprovalTimeoutSeconds int            `json:"approval_timeout_seconds"`
	Attachments            []wsAttachment `json:"attachments"`
}

// startServeRun launches a headless agent run. It mirrors the WebSocket
// prompt path exactly — same handlePrompt, same persistence, same approval
// gating — only the event sink differs.
func startServeRun(
	resolved config.ResolvedConfig,
	system string,
	store *session.Store,
	resources *resource.Registry,
	req promptRequest,
) (*serveRun, error) {
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("content required")
	}
	if len(req.Content) > maxPromptBytes {
		return nil, fmt.Errorf("prompt exceeds maximum size")
	}
	if req.Model != "" && (len(req.Model) > maxModelIDBytes || !modelIDPattern.MatchString(req.Model)) {
		return nil, fmt.Errorf("invalid model ID")
	}
	// Resource bound first: refuse before spawning an agent, MCP clients,
	// or a sandbox container. Count-then-register has a benign race — the
	// cap defends a local management surface against runaway scripts, not
	// a concurrent adversary holding the instance token.
	if activeRunCount() >= maxActiveServeRuns {
		return nil, fmt.Errorf("%w: cap is %d", errTooManyActiveRuns, maxActiveServeRuns)
	}
	// Session-token validation, same rule as every other session-scoped
	// endpoint (2026-08 audit: the AuthToken field was accepted but never
	// checked, so a cookie-only caller could resume and mutate any
	// session). A session_id that does not load is fine — handlePrompt
	// creates a fresh session. Legacy sessions without a token get one
	// minted and persisted by validateSessionToken.
	if req.SessionID != "" && store != nil {
		if sess, err := store.Load(req.SessionID); err == nil && sess != nil {
			if _, ok := validateSessionToken(store, sess, req.AuthToken); !ok {
				return nil, errInvalidSessionToken
			}
		}
	}

	run := &serveRun{
		ID:        newRunID(),
		Model:     resolved.Model,
		Status:    "running",
		StartedAt: time.Now().UTC(),
		pending:   map[string]*approvalRequest{},
	}
	run.cond = sync.NewCond(&run.mu)

	if req.Model != "" {
		resolved.Model = req.Model
		run.Model = req.Model
	}

	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel

	var deltas wsDeltaCounters
	agent, sandboxCleanup, mcpCleanup, guardCleanup, injectionGuard, approver, err := newServeAgent(resolved, system, run.ID, func(v any) error {
		// wsApprover sends its typed approvalRequest struct; everything
		// else arrives as map[string]any.
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		return run.record(v)
	}, &deltas)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("agent: %w", err)
	}

	// Headless runs may wait longer than the socket default for approvals.
	if req.ApprovalTimeoutSeconds > 0 {
		t := time.Duration(req.ApprovalTimeoutSeconds) * time.Second
		if t > maxRunApprovalWait {
			t = maxRunApprovalWait
		}
		approver.SetApprovalTimeout(t)
	}

	cleanup := func() {
		if approver != nil {
			approver.Cancel()
		}
		agent.Close() //nolint:errcheck
		if guardCleanup != nil {
			guardCleanup() //nolint:errcheck
		}
		if sandboxCleanup != nil {
			sandboxCleanup() //nolint:errcheck
		}
		if mcpCleanup != nil {
			mcpCleanup()
		}
	}
	run.mu.Lock()
	run.approver = approver
	run.cleanup = cleanup
	run.mu.Unlock()
	registerRun(run)

	msg := wsClientMsg{
		Type:        "prompt",
		Content:     req.Content,
		SessionID:   req.SessionID,
		AuthToken:   req.AuthToken,
		Model:       req.Model,
		Thinking:    req.Thinking,
		Attachments: req.Attachments,
	}

	// Approval waits are ctx-blind (see cancelRun): POST /api/cancel on the
	// run's session goes through the prompt-cancel registry and would fire
	// only the context, leaving the loop blocked until the approval ceiling.
	// Compose the approver interrupt into what handlePrompt registers.
	// run.cancel stays plain — cancelRun interrupts the approver itself, and
	// double-Cancel is safe now that Cancel re-arms.
	cancelWithApproval := func() {
		cancel()
		if approver != nil {
			approver.Cancel()
		}
	}

	go func() {
		defer cleanup()
		var sessionIn, sessionOut int
		sess := handlePrompt(ctx, func(m map[string]any) { _ = run.record(m) }, store, resources, resolved, agent, injectionGuard, nil, msg, &sessionIn, &sessionOut, cancelWithApproval, &deltas)
		run.mu.Lock()
		if sess != nil {
			run.SessionID = sess.ID
		}
		errMsg := run.Error
		run.mu.Unlock()
		if errMsg != "" {
			run.finish("failed", errMsg)
		} else {
			run.finish("completed", "")
		}
	}()

	return run, nil
}

// decodeJSONBody decodes a JSON request body with a 2 MiB cap.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(v)
}

// handlePromptStart implements POST /api/prompt.
func handlePromptStart(st *serveState, store *session.Store, resources *resource.Registry, system string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req promptRequest
		if err := decodeJSONBody(w, r, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		run, err := startServeRun(st.resolved, system, store, resources, req)
		if err != nil {
			switch {
			case errors.Is(err, errInvalidSessionToken):
				http.Error(w, err.Error(), http.StatusUnauthorized)
			case errors.Is(err, errTooManyActiveRuns):
				w.Header().Set("Retry-After", "30")
				http.Error(w, err.Error(), http.StatusTooManyRequests)
			default:
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		writeAPIJSON(w, http.StatusAccepted, map[string]any{
			"run_id":     run.ID,
			"session_id": req.SessionID,
			"status":     "running",
		})
	}
}

// handleRunList implements GET /api/runs.
func handleRunList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
		writeAPIJSON(w, http.StatusOK, map[string]any{"runs": listRunsWire(limit), "active": activeRunCount()})
	}
}

// handleRunByID implements GET/DELETE /api/runs/{id} (DELETE = cancel).
func handleRunByID() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		if id == "" {
			http.Error(w, "missing run id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodDelete:
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		run := lookupRun(id)
		if run == nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			writeAPIJSON(w, http.StatusOK, run.snapshot(true))
			return
		}
		cancelRun(run)
		writeAPIJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "run_id": id})
	}
}

// handleRunApprovalList implements GET /api/runs/{id}/approvals.
func handleRunApprovalList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/approvals")
		run := lookupRun(id)
		if run == nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		snap := run.snapshot(false)
		writeAPIJSON(w, http.StatusOK, map[string]any{
			"run_id":            id,
			"pending_approvals": snap["pending_approvals"],
		})
	}
}

// handleRunApprovalAnswer implements POST /api/runs/{id}/approvals/{aid}.
// The answer is delivered through wsApprover.HandleResponse — the same path
// the WebSocket reader uses — so trust caching and friction behave
// identically for REST and socket clients.
func handleRunApprovalAnswer() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[1] != "approvals" {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		runID, approvalID := parts[0], parts[2]
		run := lookupRun(runID)
		if run == nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		var body struct {
			Action string `json:"action"`
		}
		if err := decodeJSONBody(w, r, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		switch body.Action {
		case "approve", "deny", "trust":
		default:
			http.Error(w, "action must be approve, deny, or trust", http.StatusBadRequest)
			return
		}
		run.mu.Lock()
		approver := run.approver
		run.mu.Unlock()
		if approver == nil {
			http.Error(w, "run has no approver", http.StatusConflict)
			return
		}
		if !approver.HandleResponse(approvalID, body.Action) {
			http.Error(w, "approval not found (it may have timed out)", http.StatusNotFound)
			return
		}
		// Mirror the ack + pending-list update the socket path gets.
		_ = run.record(map[string]any{"type": "approval_ack", "id": approvalID, "action": body.Action})
		writeAPIJSON(w, http.StatusOK, map[string]any{"run_id": runID, "approval_id": approvalID, "action": body.Action})
	}
}

// handleRunCancel implements POST /api/runs/{id}/cancel.
func handleRunCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/cancel")
		run := lookupRun(id)
		if run == nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		idle := run.isTerminal()
		if !idle {
			cancelRun(run)
		}
		resp := map[string]any{"run_id": id, "status": run.snapshot(false)["status"]}
		if idle {
			resp["idle"] = true
		}
		writeAPIJSON(w, http.StatusOK, resp)
	}
}

func cancelRun(run *serveRun) {
	run.mu.Lock()
	cancel := run.cancel
	approver := run.approver
	run.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Approval waits are ctx-blind (wsApprover selects only on response /
	// its own cancel channel / timeout), so cancelling the run context alone
	// leaves the loop blocked in PromptCommand until the approval ceiling
	// (up to maxRunApprovalWait). Interrupt the approver NOW instead of
	// waiting for the deferred cleanup, which only runs after handlePrompt
	// unblocks.
	if approver != nil {
		approver.Cancel()
	}
	run.finish("cancelled", "")
}
