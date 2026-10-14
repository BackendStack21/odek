package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/session"
)

// GET /api/usage promises prompts_started / prompts_completed / prompts_failed
// aggregates, and the B3-SERVE-1 fix made "a run that ends with status=failed"
// bump prompts_failed. handlePrompt increments PromptsStarted before the
// session/reference/attachment validation, but every early sendError return
// after that point (unknown @sess reference, invalid reference token, session
// not found, recovery mismatch, attachment errors, expanded-prompt cap) leaves
// PromptsFailed untouched. The REST run is reported failed, yet the lifetime
// aggregate shows a started prompt that neither completed nor failed.
func TestRED_ServeUsageEarlyFailedRunNotCountedAsFailed(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"never reached"}}]}`))
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	resetServeUsageForTest()
	t.Cleanup(resetServeUsageForTest)

	// A well-formed but unknown session id in an @sess reference fails the
	// turn before any LLM call.
	_, resp := startTestRun(t, env, `{"content":"compare with @sess:20260101-000000-0123456789abcdef0123456789abcdef"}`)
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run_id: %v", resp)
	}
	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "failed" {
		t.Fatalf("precondition: status = %v (error %v), want failed", snap["status"], snap["error"])
	}

	w := httptest.NewRecorder()
	handleUsage(env.state.resolved)(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	started, _ := body["prompts_started"].(float64)
	failed, _ := body["prompts_failed"].(float64)
	completed, _ := body["prompts_completed"].(float64)
	if started != 1 {
		t.Fatalf("precondition: prompts_started = %v, want 1", started)
	}
	if failed != 1 || atomic.LoadInt64(&serveStats.PromptsFailed) != 1 {
		t.Fatalf("run %s failed (%v) but /api/usage reports prompts_failed=%v, prompts_completed=%v, prompts_started=%v",
			runID, snap["error"], failed, completed, started)
	}
}

// POST /api/uploads without session_id creates a fresh "Uploaded files"
// session BEFORE the retention-capacity check (and before the upload storage
// is opened). docs/WEBUI.md: "new uploads are rejected if pinned files leave
// insufficient capacity" — the request is rejected with 503 and no upload id
// or token is returned, yet the freshly created session stays on disk and in
// the session list forever: every rejected upload leaves an empty orphan
// conversation the client never learns about.
func TestRED_ServeRejectedUploadLeavesOrphanSession(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Fill the workspace's upload capacity with pinned handles (active turns).
	browserUploads.Lock()
	saved := browserUploads.entries
	browserUploads.entries = map[string]browserUpload{}
	for i := 0; i < uploadHandleLimit; i++ {
		browserUploads.entries[fmt.Sprintf("pinned-%d", i)] = browserUpload{
			sessionID: "20260101-000000-pinned", path: filepath.Join(workspace, "x", fmt.Sprint(i)),
			name: "p.png", workspace: workspace, created: time.Now(), size: 1, pins: 1,
		}
	}
	browserUploads.Unlock()
	t.Cleanup(func() {
		browserUploads.Lock()
		browserUploads.entries = saved
		browserUploads.Unlock()
	})

	before, _ := store.List(0)
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0}
	req := httptest.NewRequest(http.MethodPost, "/api/uploads?name=image.png", bytes.NewReader(png))
	rec := httptest.NewRecorder()
	handleBrowserUpload(store, "model", workspace)(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("precondition: upload accepted despite full pinned capacity: %s", rec.Body.String())
	}
	after, _ := store.List(0)
	if len(after) != len(before) {
		var titles []string
		for _, s := range after {
			titles = append(titles, s.ID+" "+s.Task)
		}
		t.Fatalf("rejected upload (%d %q) left %d new orphan session(s): %v",
			rec.Code, strings.TrimSpace(rec.Body.String()), len(after)-len(before), titles)
	}
}

// The socket reader answers ping with a pong carrying "the server_info
// snapshot fields", and the WebUI health popover renders pong.model as the
// connection's model (health.js onPong). The snapshot is taken once at
// connect (snapshotServerConfig) to avoid a data race, so after a per-prompt
// model switch every heartbeat (20s) re-reports the model the connection
// started with: the popover reverts to the stale model while the connection
// registry (/api/connections) and the session frame report the new one.
func TestRED_ServePongReportsStaleModelAfterSwitch(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxV2(t, store, nil)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()
	hello := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })
	startModel, _ := hello["model"].(string)
	const switched = "serve-hunt13-switched-model"
	if startModel == switched {
		t.Fatalf("precondition: start model already %q", switched)
	}

	writeJSON(conn, map[string]any{"type": "prompt", "content": "hello", "model": switched})
	sessFrame := readWSUntil(t, conn, 20*time.Second, func(e map[string]any) bool { return e["type"] == "session" })
	if sessFrame["model"] != switched {
		t.Fatalf("precondition: session frame model = %v, want %q", sessFrame["model"], switched)
	}
	readWSUntil(t, conn, 20*time.Second, func(e map[string]any) bool { return e["type"] == "done" || e["type"] == "error" })

	writeJSON(conn, map[string]any{"type": "ping"})
	pong := readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "pong" })
	if pong["model"] != switched {
		t.Fatalf("pong.model = %v after switching the connection to %q (connection started on %q)", pong["model"], switched, startModel)
	}
}

// requireLocalHost strips the port with net.SplitHostPort and compares the
// remainder against "localhost", "127.0.0.1" and "::1". An IPv6 Host header
// without a port keeps its brackets ("[::1]" — the form browsers and curl
// send for http://[::1]/ on port 80), SplitHostPort fails, and the bracketed
// literal never equals "::1": every /api call to a serve bound on [::1]:80 is
// rejected 403 "Host not allowed" although it is loopback. The Origin check
// (originHostMatchesRequest) already trims the brackets for the same input.
func TestRED_ServeLocalHostRejectsBracketedIPv6WithoutPort(t *testing.T) {
	h := requireLocalHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, host := range []string{"[::1]:8080", "[::1]"} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("loopback Host %q rejected: %d %s", host, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// The REST run registry keeps every run's event tail in memory (newest 100
// runs, at least 20 completed) and bounds the tail by COUNT only
// (serveRunEventsKept = 200). The tail records the agent's live frames
// verbatim, including tool_call arguments (model-chosen) and tool_result
// data: ToolDetailHandler fires with the FULL tool output before the loop's
// 4 KiB context clip, so each frame can carry a whole shell stream (1 MiB),
// file read or MCP result. A single run that reads a few large files or web
// pages pins hundreds of MiB, retained after the run finishes, and the
// registry holds up to 100 such runs — unbounded-by-bytes memory growth
// driven by tool output (hostile content) in a long-lived serve process.
func TestRED_ServeRunEventTailUnboundedBytes(t *testing.T) {
	run := &serveRun{ID: "run-hunt13-tail", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	big := strings.Repeat("A", 1<<20) // one 1 MiB shell stream
	for i := 0; i < serveRunEventsKept; i++ {
		_ = run.record(map[string]any{"type": "tool_result", "name": "shell", "data": big, "call_id": fmt.Sprint(i)})
	}
	run.finish("completed", "")

	retained := 0
	run.mu.Lock()
	for _, ev := range run.events {
		if s, ok := ev["data"].(string); ok {
			retained += len(s)
		}
	}
	run.mu.Unlock()
	const budget = 16 << 20
	if retained > budget {
		t.Fatalf("completed run retains %d MiB of tool output in its event tail (budget %d MiB); the registry keeps up to %d runs",
			retained>>20, budget>>20, serveRunsCap)
	}
}

// docs/SECURITY.md: rejected session lookups (unknown id or bad token) on
// every session-scoped endpoint are rate-limited to 60/min per IP, so none is
// an unthrottled existence oracle (prior fixes covered detail, delete,
// rename, recovery, cancel, jobs, artifacts, uploads, sub-agents and the WS
// frames). POST /api/prompt validates session_id only when the session
// LOADS: an existing id with a bad token is 401 and, once the per-IP budget
// is spent, 429 — but an unknown id is accepted with 202 and a full agent
// (MCP servers, sandbox container) is built before handlePrompt fails the
// run with "session not found". With the budget exhausted the answer is
// still 202 for unknown ids versus 429 for existing ones.
func TestRED_ServePromptStartUnknownSessionAcceptedOracle(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"never reached"}}]}`))
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)
	sessionLookupLimiter.reset()
	t.Cleanup(sessionLookupLimiter.reset)
	t.Cleanup(resetServeRuns)

	existing, err := env.store.Create(nil, "m", "victim")
	if err != nil {
		t.Fatal(err)
	}
	post := func(sessionID string) (int, string) {
		body := fmt.Sprintf(`{"content":"hi","session_id":%q,"auth_token":"wrong"}`, sessionID)
		req := httptest.NewRequest(http.MethodPost, "/api/prompt", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.13:5555"
		rec := httptest.NewRecorder()
		handlePromptStart(env.state, env.store, env.resources, env.system)(rec, req)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	// Spend the per-IP rejection budget on the existing session.
	var lastExisting int
	for i := 0; i < 70; i++ {
		lastExisting, _ = post(existing.ID)
	}
	if lastExisting != http.StatusTooManyRequests {
		t.Fatalf("precondition: existing-session probes not limited (last %d)", lastExisting)
	}
	code, body := post("20260101-000000-00000000000000000000000000000abc")
	if code == http.StatusAccepted {
		t.Fatalf("unknown session_id accepted with 202 (%s) and a run was spawned, while existing ids answer %d: unthrottled existence oracle and agent build for a session that does not exist", body, lastExisting)
	}
}

// docs/WEBUI.md: approval_timeout_seconds is "approval wait (default 60s,
// cap 600s)". startServeRun multiplies the raw int by time.Second BEFORE
// clamping: a large value overflows int64 nanoseconds and wraps. Negative
// wraps fall back to the default, but 18446744074 wraps to +0.29s, which is
// below the cap and is installed as the approval timeout — every approval
// of the run expires (is denied) before a poller can answer it.
func TestRED_ServeRunApprovalTimeoutOverflowWraps(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)
	t.Cleanup(resetServeRuns)

	_, resp := startTestRun(t, env, `{"content":"hi","approval_timeout_seconds":18446744074}`)
	runID, _ := resp["run_id"].(string)
	run := lookupRun(runID)
	if run == nil {
		t.Fatalf("run %q not registered", runID)
	}
	run.mu.Lock()
	approver := run.approver
	run.mu.Unlock()
	approver.mu.Lock()
	got := approver.approvalTimeout
	approver.mu.Unlock()
	waitRunStatus(t, runID, 30*time.Second)
	if got < 60*time.Second || got > maxRunApprovalWait {
		t.Fatalf("approval_timeout_seconds=18446744074 installed an approval timeout of %v (want the 600s cap)", got)
	}
}

// docs/WEBUI.md: GET /api/schedules/preview returns the "validated
// cron/timezone and next three scheduled times". The preview only parses the
// expression; it skips the never-matches check schedule.Job.Validate applies
// (the store rejects "0 0 30 2 *" — Feb 30 — with "never matches a real
// date"). For such an expression the preview answers 200 with three zero
// times (0001-01-01T00:00:00Z), so the UI shows bogus run times for a
// schedule that saving will then refuse.
func TestRED_ServeSchedulePreviewAcceptsNeverMatchingCron(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/preview?cron=0+0+30+2+*&timezone=UTC", nil)
	rec := httptest.NewRecorder()
	handleSchedulePreview(rec, req)
	if rec.Code == http.StatusOK {
		var body struct {
			Next []time.Time `json:"next"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		t.Fatalf("preview accepted a never-matching cron: 200 next=%v (store.Add rejects it)", body.Next)
	}
}
