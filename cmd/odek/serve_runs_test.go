package main

// Tests for the round-2 management surface (TEMP_SERVE_IMPROVEMENT_PLAN.md
// Phases E–G): headless REST runs + the remote approval bridge, the runtime
// events ring, usage aggregates, the connection registry, the sanitized
// config view, MCP listing, skill promotion, session pinning, and memory
// consolidation.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/mcpclient"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/skills"
)

// ── Events ring ───────────────────────────────────────────────────────

func TestEventsRing_AddSnapshotFilterAndCap(t *testing.T) {
	r := &eventsRing{}
	base := time.Now().UTC()
	for i := 0; i < serveEventsCap+50; i++ {
		r.add(events.Event{Type: "e", RunID: fmt.Sprintf("run-%d", i), Timestamp: base})
	}
	all := r.snapshot(serveEventsCap, "", "")
	if len(all) != serveEventsCap {
		t.Fatalf("ring holds %d, want cap %d", len(all), serveEventsCap)
	}
	// The oldest 50 were dropped.
	if all[0].RunID != "run-50" {
		t.Errorf("oldest kept = %s, want run-50", all[0].RunID)
	}
	// Run filter.
	got := r.snapshot(10, "run-100", "")
	if len(got) != 1 || got[0].RunID != "run-100" {
		t.Errorf("run filter returned %v", got)
	}
	// Session filter excludes everything.
	if got := r.snapshot(10, "", "sess"); len(got) != 0 {
		t.Errorf("session filter returned %d events, want 0", len(got))
	}
}

func TestHandleEvents_LimitAndFilters(t *testing.T) {
	serveEvents.reset()
	t.Cleanup(serveEvents.reset)
	now := time.Now().UTC()
	serveEvents.add(events.Event{Type: "run_started", RunID: "r1", SessionID: "s1", Timestamp: now})
	serveEvents.add(events.Event{Type: "run_completed", RunID: "r1", SessionID: "s1", Timestamp: now})
	serveEvents.add(events.Event{Type: "run_started", RunID: "r2", Timestamp: now})

	w := httptest.NewRecorder()
	handleEvents()(w, httptest.NewRequest(http.MethodGet, "/api/events?limit=1", nil))
	var body struct {
		Events []events.Event `json:"events"`
		Count  int            `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Events) != 1 {
		t.Fatalf("limit=1 returned %d events", body.Count)
	}

	w = httptest.NewRecorder()
	handleEvents()(w, httptest.NewRequest(http.MethodGet, "/api/events?run_id=r1", nil))
	body.Count, body.Events = 0, nil
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Fatalf("run_id=r1 returned %d events, want 2", body.Count)
	}

	w = httptest.NewRecorder()
	handleEvents()(w, httptest.NewRequest(http.MethodPost, "/api/events", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}

// ── Usage aggregates ──────────────────────────────────────────────────

func TestHandleUsage_CountersAndCostFlag(t *testing.T) {
	resetServeUsageForTest()
	t.Cleanup(resetServeUsageForTest)
	atomic.AddInt64(&serveStats.PromptsStarted, 3)
	atomic.AddInt64(&serveStats.PromptsCompleted, 2)
	atomic.AddInt64(&serveStats.TokensIn, 1_000_000)
	atomic.AddInt64(&serveStats.TokensOut, 500_000)

	resolved := config.ResolvedConfig{Model: "m"}
	w := httptest.NewRecorder()
	handleUsage(resolved)(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["prompts_started"].(float64) != 3 || body["tokens_in"].(float64) != 1e6 {
		t.Errorf("usage body = %v", body)
	}
	if body["prices_configured"] != false {
		t.Errorf("prices_configured = %v, want false without prices", body["prices_configured"])
	}
}

func resetServeUsageForTest() {
	atomic.StoreInt64(&serveStats.PromptsStarted, 0)
	atomic.StoreInt64(&serveStats.PromptsCompleted, 0)
	atomic.StoreInt64(&serveStats.PromptsFailed, 0)
	atomic.StoreInt64(&serveStats.TokensIn, 0)
	atomic.StoreInt64(&serveStats.TokensOut, 0)
}

// ── Connection registry ───────────────────────────────────────────────

func TestWSConnRegistry_RegisterListKick(t *testing.T) {
	c1 := &wsConnInfo{ID: "conn-a", ConnectedAt: time.Now().Add(-time.Second)}
	c2 := &wsConnInfo{ID: "conn-b", ConnectedAt: time.Now()}
	wsConnRegister(c1)
	wsConnRegister(c2)
	t.Cleanup(func() {
		wsConnUnregister("conn-a")
		wsConnUnregister("conn-b")
	})

	c1.setLive("sess-1", true)
	c1.recordPrompt()

	list := wsConnList()
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID != "conn-a" { // oldest first
		t.Errorf("order = [%s, %s], want conn-a first", list[0].ID, list[1].ID)
	}
	if list[0].SessionID != "sess-1" || !list[0].Busy || list[0].Prompts != 1 {
		t.Errorf("conn-a wire state = id=%s sess=%s busy=%v prompts=%d", list[0].ID, list[0].SessionID, list[0].Busy, list[0].Prompts)
	}

	// Kicking an unregistered id fails; a registered one succeeds even
	// without a socket (kick tolerates nil conn).
	if wsConnKick("conn-nope") {
		t.Error("kick of unknown id succeeded")
	}
	if !wsConnKick("conn-b") {
		t.Error("kick of registered id failed")
	}
}

func TestHandleConnections_ListShape(t *testing.T) {
	c := &wsConnInfo{ID: "conn-x", RemoteAddr: "127.0.0.1:1", ConnectedAt: time.Now().UTC()}
	wsConnRegister(c)
	t.Cleanup(func() { wsConnUnregister("conn-x") })

	w := httptest.NewRecorder()
	handleConnections()(w, httptest.NewRequest(http.MethodGet, "/api/connections", nil))
	var body struct {
		Connections []json.RawMessage `json:"connections"`
		Count       int               `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count < 1 {
		t.Fatalf("count = %d, want >= 1", body.Count)
	}
}

func TestHandleConnectionKick_NotFoundAndMissing(t *testing.T) {
	w := httptest.NewRecorder()
	handleConnectionKick()(w, httptest.NewRequest(http.MethodDelete, "/api/connections/conn-nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	handleConnectionKick()(w, httptest.NewRequest(http.MethodDelete, "/api/connections/", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ── Config view sanitization ──────────────────────────────────────────

func TestHandleConfigView_NoSecrets(t *testing.T) {
	resolved := config.ResolvedConfig{Model: "m", Stream: true}
	resolved.BaseURL = "https://secret-endpoint.example/v1"
	resolved.APIKey = "sk-leak-me"
	w := httptest.NewRecorder()
	handleConfigView(resolved)(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	body := w.Body.String()
	for _, secret := range []string{"secret-endpoint", "sk-leak-me"} {
		if strings.Contains(body, secret) {
			t.Errorf("config view leaked %q", secret)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed["model"] != "m" || parsed["stream"] != true {
		t.Errorf("config view = %v", parsed)
	}
	sb, _ := parsed["sandbox"].(map[string]any)
	if sb == nil || sb["enabled"] != false {
		t.Errorf("sandbox section = %v", parsed["sandbox"])
	}
}

// ── MCP listing ───────────────────────────────────────────────────────

func TestHandleMCPServers_ListsServersWithoutEnv(t *testing.T) {
	resolved := config.ResolvedConfig{
		MCPServers: map[string]mcpclient.ServerConfig{
			"local": {
				Command: "node",
				Args:    []string{"server.js"},
				Env:     map[string]string{"TOKEN": "sk-mcp-secret"},
			},
		},
		ProjectMCPServerNames: []string{"local"},
	}
	w := httptest.NewRecorder()
	handleMCPServers(resolved)(w, httptest.NewRequest(http.MethodGet, "/api/mcp", nil))
	body := w.Body.String()
	if strings.Contains(body, "sk-mcp-secret") {
		t.Error("mcp listing leaked env values")
	}
	var parsed struct {
		Servers []map[string]any `json:"servers"`
		Count   int              `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Count != 1 {
		t.Fatalf("count = %d, want 1", parsed.Count)
	}
	if parsed.Servers[0]["command"] != "node" || parsed.Servers[0]["project"] != true {
		t.Errorf("server entry = %v", parsed.Servers[0])
	}
}

// ── Session pinning ───────────────────────────────────────────────────

func TestHandleSessionByID_PostPinAndListOrdering(t *testing.T) {
	store := newTestSessionStore(t)
	older, err := store.Create([]llm.Message{{Role: "user", Content: "a"}}, "m", "older")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.Create([]llm.Message{{Role: "user", Content: "b"}}, "m", "newer")
	if err != nil {
		t.Fatal(err)
	}
	handler := handleSessionByID(store, nil, "")

	// Pin the OLDER session — it must float above the newer one.
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+older.ID, strings.NewReader(`{"pinned":true}`))
	req.Header.Set("X-Session-Token", older.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pin status = %d (body: %s)", w.Code, w.Body.String())
	}
	var updated session.Session
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Pinned {
		t.Error("pinned flag not set in response")
	}

	// Legacy rename-only body still works.
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+newer.ID, strings.NewReader(`{"name":"renamed"}`))
	req.Header.Set("X-Session-Token", newer.AuthToken)
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status = %d", w.Code)
	}

	// Empty body is rejected.
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+newer.ID, strings.NewReader(`{}`))
	req.Header.Set("X-Session-Token", newer.AuthToken)
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", w.Code)
	}

	// List returns pinned first even though "newer" sorts first by time.
	w = httptest.NewRecorder()
	handleSessionListPaged(store)(w, httptest.NewRequest(http.MethodGet, "/api/sessions?limit=10&offset=0", nil))
	var page struct {
		Sessions []session.Session `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 2 || page.Sessions[0].ID != older.ID {
		t.Fatalf("ordering = %v", page.Sessions)
	}
}

// ── Run registry eviction ─────────────────────────────────────────────

func TestRegisterRun_EvictsOldestCompleted(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	fill := func(n int, base time.Time) {
		for i := 0; i < n; i++ {
			r := &serveRun{
				ID:        fmt.Sprintf("run-evict-%d-%d", base.UnixNano(), i),
				Status:    "completed",
				StartedAt: base.Add(time.Duration(i) * time.Second),
				EndedAt:   base.Add(time.Duration(i) * time.Second).Add(time.Second),
				pending:   map[string]*approvalRequest{},
			}
			r.cond = sync.NewCond(&r.mu)
			registerRun(r)
		}
	}
	fill(serveRunsCap+10, time.Now().Add(-time.Hour))
	serveRuns.mu.Lock()
	n := len(serveRuns.runs)
	serveRuns.mu.Unlock()
	if n > serveRunsCap {
		t.Fatalf("registry holds %d runs, want <= %d", n, serveRunsCap)
	}
	// The evicted ones must be the oldest.
	if lookupRun("run-evict-...-0") != nil {
		// ids are time-derived; spot-check via list ordering instead
	}
	first := listRunsWire(1)[0]
	st, _ := first["started_at"].(time.Time)
	if st.Before(time.Now().Add(-time.Hour).Add(time.Duration(serveRunsCap) * time.Second)) {
		t.Errorf("newest run kept is too old: %v", st)
	}
}

// ── Headless REST runs (E2E) ─────────────────────────────────────────

// restRunEnv bundles the pieces a REST-run test needs.
type restRunEnv struct {
	store     *session.Store
	resources *resource.Registry
	resolved  config.ResolvedConfig
	state     *serveState
	system    string
}

func newRestRunEnv(t *testing.T, llmURL string, mutate func(*config.ResolvedConfig)) *restRunEnv {
	t.Helper()
	cleanup := setTestEnv(t, llmURL)
	t.Cleanup(cleanup)
	store := newTestSessionStore(t)
	resolved := config.LoadConfig(config.CLIFlags{})
	if resolved.System == "" {
		resolved.System = defaultSystem
	}
	if mutate != nil {
		mutate(&resolved)
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	res := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".odek", "sessions")),
	)
	return &restRunEnv{
		store:     store,
		resources: res,
		resolved:  resolved,
		state:     &serveState{startedAt: time.Now(), resolved: resolved},
		system:    resolved.System,
	}
}

// startTestRun drives handlePromptStart directly.
func startTestRun(t *testing.T, env *restRunEnv, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/prompt", strings.NewReader(body))
	w := httptest.NewRecorder()
	handlePromptStart(env.state, env.store, env.resources, env.system)(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d (body: %s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return w.Code, resp
}

// waitRunStatus polls the run registry until the run reaches a terminal
// status or the deadline passes.
func waitRunStatus(t *testing.T, runID string, deadline time.Duration) map[string]any {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		run := lookupRun(runID)
		if run == nil {
			t.Fatal("run disappeared from registry")
		}
		snap := run.snapshot(true)
		if s, _ := snap["status"].(string); runStatusTerminal(s) {
			return snap
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish within %v (last: %v)", runID, deadline, lookupRun(runID).snapshot(false))
	return nil
}

func TestServe_E2E_RestRunLifecycle(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"REST answer"}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`))
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	resetServeUsageForTest()
	t.Cleanup(resetServeUsageForTest)

	_, resp := startTestRun(t, env, `{"content":"say something"}`)
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run_id in response: %v", resp)
	}

	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "completed" {
		t.Fatalf("status = %v, want completed (error: %v)", snap["status"], snap["error"])
	}
	if snap["result"] != "REST answer" {
		t.Errorf("result = %v", snap["result"])
	}
	if snap["session_id"] == "" || snap["session_id"] == nil {
		t.Errorf("run did not record its session id: %v", snap["session_id"])
	}

	// The created session carries cumulative usage.
	sessID, _ := snap["session_id"].(string)
	sess, err := env.store.Load(sessID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.InputTokens == 0 {
		t.Error("session input tokens not persisted")
	}

	// Usage aggregates were bumped.
	if got := atomic.LoadInt64(&serveStats.PromptsCompleted); got < 1 {
		t.Errorf("prompts_completed = %d, want >= 1", got)
	}
	if got := atomic.LoadInt64(&serveStats.TokensIn); got == 0 {
		t.Error("tokens_in not accumulated")
	}

	// The run appears in the list.
	w := httptest.NewRecorder()
	handleRunList()(w, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if !strings.Contains(w.Body.String(), runID) {
		t.Errorf("run list missing %s: %s", runID, w.Body.String())
	}

	// Runtime events flowed into the ring for this run.
	evs := serveEvents.snapshot(serveEventsCap, "", sessID)
	if len(evs) == 0 {
		t.Error("no odek.event/v1 events captured for the REST run")
	}
}

func TestServe_E2E_RestRunValidation(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)

	handler := handlePromptStart(env.state, env.store, env.resources, env.system)

	for name, body := range map[string]string{
		"empty content": `{}`,
		"oversized":     `{"content":"` + strings.Repeat("x", maxPromptBytes+10) + `"}`,
		"bad model":     `{"content":"hi","model":"bad id!"}`,
		"invalid json":  `not json`,
	} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/api/prompt", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
	}
}

func TestServe_E2E_RestRunApprovalBridge(t *testing.T) {
	// The model requests a shell command; the danger policy forces every
	// class to prompt, so the run blocks in wsApprover until the REST
	// bridge answers.
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Running.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo bridge-ok\"}"}}]}}]}`)
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"Approved and done."}}]}`))
		}
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, func(rc *config.ResolvedConfig) {
		prompt := "prompt"
		rc.Dangerous.DefaultAction = &prompt
	})

	// Long approval window so the test has time to answer.
	_, resp := startTestRun(t, env, `{"content":"run a command","approval_timeout_seconds":120}`)
	runID, _ := resp["run_id"].(string)

	// Wait for the pending approval to appear.
	var pending []map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		run := lookupRun(runID)
		if run == nil {
			t.Fatal("run vanished")
		}
		snap := run.snapshot(false)
		if s, _ := snap["status"].(string); s == "waiting_approval" {
			pending, _ = snap["pending_approvals"].([]map[string]any)
			if len(pending) > 0 {
				break
			}
		}
		if s, _ := snap["status"].(string); runStatusTerminal(s) {
			t.Fatalf("run finished without asking for approval: %v", snap)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(pending) == 0 {
		t.Fatal("no pending approval appeared")
	}
	approvalID, _ := pending[0]["id"].(string)
	if cmd, _ := pending[0]["command"].(string); !strings.Contains(cmd, "echo bridge-ok") {
		t.Errorf("approval command = %q", cmd)
	}

	// Answer it via the REST bridge.
	answer := handleRunApprovalAnswer()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/approvals/"+approvalID, strings.NewReader(`{"action":"approve"}`))
	w := httptest.NewRecorder()
	answer(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d (body: %s)", w.Code, w.Body.String())
	}

	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "completed" {
		t.Fatalf("status = %v (error: %v)", snap["status"], snap["error"])
	}
	if snap["result"] != "Approved and done." {
		t.Errorf("result = %v", snap["result"])
	}

	// Answering again (stale) is a 404.
	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/approvals/"+approvalID, strings.NewReader(`{"action":"deny"}`))
	w = httptest.NewRecorder()
	answer(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("stale answer status = %d, want 404", w.Code)
	}
}

func TestServe_E2E_RestRunCancel(t *testing.T) {
	// Slow LLM: holds the run open so cancel has something to cancel.
	block := make(chan struct{})
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		<-block
	}))
	defer llmSrv.Close()
	defer close(block)

	env := newRestRunEnv(t, llmSrv.URL, nil)
	_, resp := startTestRun(t, env, `{"content":"slow one"}`)
	runID, _ := resp["run_id"].(string)

	// Cancel via the dedicated endpoint.
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/cancel", nil)
	w := httptest.NewRecorder()
	handleRunCancel()(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d (body: %s)", w.Code, w.Body.String())
	}

	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "cancelled" {
		t.Fatalf("status = %v, want cancelled", snap["status"])
	}

	// Cancelling again reports idle.
	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/cancel", nil)
	w = httptest.NewRecorder()
	handleRunCancel()(w, req)
	if !strings.Contains(w.Body.String(), `"idle":true`) {
		t.Errorf("second cancel body = %s", w.Body.String())
	}
}

func TestHandleRunByID_NotFoundAndMethods(t *testing.T) {
	w := httptest.NewRecorder()
	handleRunByID()(w, httptest.NewRequest(http.MethodGet, "/api/runs/run-nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	handleRunByID()(w, httptest.NewRequest(http.MethodPut, "/api/runs/run-whatever", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ── Skill promotion ───────────────────────────────────────────────────

func TestHandleSkillPromote_ClearsNeedsReview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userSkills := filepath.Join(home, ".odek", "skills")
	skillDir := filepath.Join(userSkills, "promote-me")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: promote-me\ndescription: x\nodek:\n  provenance:\n    needs_review: true\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/skills/promote", strings.NewReader(`{"name":"promote-me"}`))
	w := httptest.NewRecorder()
	handleSkillPromote()(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body: %s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handleSkills(skills.SkillsConfig{})(w, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if strings.Contains(w.Body.String(), `"needs_review":true`) {
		t.Error("skill still flagged needs_review after promotion")
	}

	// Unknown skill → 400.
	req = httptest.NewRequest(http.MethodPost, "/api/skills/promote", strings.NewReader(`{"name":"ghost"}`))
	w = httptest.NewRecorder()
	handleSkillPromote()(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown skill status = %d, want 400", w.Code)
	}
}

// ── Memory consolidation (mocked LLM) ─────────────────────────────────

func TestHandleMemoryConsolidate_MergesViaLLM(t *testing.T) {
	dir := newTestMemoryDir(t)
	enabled := true
	mm := memory.NewMemoryManager(dir, nil, memory.MemoryConfig{Enabled: &enabled})
	if err := mm.AddFact("user", "likes tea in the morning"); err != nil {
		t.Fatal(err)
	}
	if err := mm.AddFact("user", "drinks tea every morning"); err != nil {
		t.Fatal(err)
	}

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"[\"drinks tea every morning\"]"}}]}`))
	}))
	defer llmSrv.Close()

	resolved := config.ResolvedConfig{BaseURL: llmSrv.URL, APIKey: "sk-mock", Model: "m"}
	req := httptest.NewRequest(http.MethodPost, "/api/memory/consolidate", strings.NewReader(`{"target":"user"}`))
	w := httptest.NewRecorder()
	handleMemoryConsolidate(dir, resolved)(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body: %s)", w.Code, w.Body.String())
	}

	userFacts, _, err := memory.NewMemoryManager(dir, nil, memory.MemoryConfig{Enabled: &enabled}).ReadFacts()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(userFacts, "likes tea in the morning") {
		t.Errorf("consolidation did not merge facts: %q", userFacts)
	}
}

// ── Session usage fields round-trip ───────────────────────────────────

func TestSession_UsageFieldsSerialize(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "usage")
	if err != nil {
		t.Fatal(err)
	}
	sess.InputTokens = 1234
	sess.OutputTokens = 567
	sess.Pinned = true
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	back, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.InputTokens != 1234 || back.OutputTokens != 567 || !back.Pinned {
		t.Errorf("round-trip lost fields: in=%d out=%d pinned=%v", back.InputTokens, back.OutputTokens, back.Pinned)
	}
}
