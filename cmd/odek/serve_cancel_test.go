package main

// Tests for the Web-UI cancellation contract (fix/webui-loop-cancel):
//
// Approval waits are ctx-blind — wsApprover.PromptCommand selects only on
// {response, its own cancel channel, timeout} and never observes the prompt
// context. Every test here drives a prompt into an approval wait (or the
// pre-registration setup window) and asserts that each cancel entry point
// (WS cancel message, POST /api/cancel, POST /api/runs/{id}/cancel) breaks
// the wait promptly and reports honestly, instead of leaving the loop
// blocked until the 60s socket / 10min headless approval ceiling.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// cancelTestTimeout bounds every wait that a regression would blow. The
// approval timeouts it must beat are 60s (socket default) and whatever the
// headless run requests — both far above this ceiling.
const cancelTestTimeout = 15 * time.Second

// buildServeMuxWithResolvers is buildServeMuxPromptAll with an explicit
// resource registry, so tests can inject resolvers with controllable Load
// behavior (e.g. block inside handlePrompt's @-ref resolution window).
func buildServeMuxWithResolvers(t *testing.T, store *session.Store, resReg *resource.Registry) (net.Listener, *http.ServeMux) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	resolved := config.LoadConfig(config.CLIFlags{})
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}
	prompt := "prompt"
	resolved.Dangerous.DefaultAction = &prompt

	wsToken, err := newServeToken()
	if err != nil {
		t.Fatalf("CSRF token: %v", err)
	}
	testTokenMu.Lock()
	testLastToken = wsToken
	testTokenMu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic(wsToken))
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(cfg *golangws.Config, req *http.Request) error {
			return wsHandshakeWithLimits(cfg, req, wsToken, nil)
		},
		Handler: func(conn *golangws.Conn) {
			handleWS(store, resReg, resolved, systemMessage, nil, conn)
		},
	})
	mux.HandleFunc("/api/cancel", handleCancel(store))

	return ln, mux
}

// startApprovalWaitPrompt sends a prompt whose first LLM response calls a
// shell tool (the danger policy forces every class to prompt), then reads
// events until the approval_request arrives. Returns the session id/token
// captured from the session event along with the approval id.
func startApprovalWaitPrompt(t *testing.T, conn *golangws.Conn) (sessionID, authToken, approvalID string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"type": "prompt", "content": "run a command"})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(cancelTestTimeout))
	for i := 0; i < 50; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v", i, err)
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		switch evt["type"] {
		case "session":
			sessionID, _ = evt["session_id"].(string)
			authToken, _ = evt["auth_token"].(string)
		case "approval_request":
			approvalID, _ = evt["id"].(string)
			if sessionID != "" && approvalID != "" {
				return sessionID, authToken, approvalID
			}
		case "error":
			t.Fatalf("unexpected error while waiting for approval: %v", evt["message"])
		}
	}
	t.Fatal("no approval_request within deadline")
	return "", "", ""
}

// readWSTerminal consumes events until a terminal one (error/done) arrives
// before the connection deadline. A regression that leaves the loop blocked
// in the approval wait surfaces as a deadline failure here.
func readWSTerminal(t *testing.T, conn *golangws.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(cancelTestTimeout))
	for i := 0; i < 100; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive while waiting for terminal event: %v", err)
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		switch evt["type"] {
		case "error", "done":
			return evt
		}
	}
	t.Fatal("no terminal event before deadline — cancel did not unwind the loop")
	return nil
}

// approvalLLM answers call 1 with a shell tool call; later calls finish the
// turn (only reached if the approval was granted).
func approvalLLM(t *testing.T) *mockLLMServer {
	t.Helper()
	return mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Running.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo cancelled-test\"}"}}]}}]}`)
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"All done."}}]}`))
		}
	})
}

// TestServe_E2E_WSCancelInterruptsApprovalWait cancels via the WS cancel
// message while the loop blocks in the approval wait. The cancelled reply
// must not be idle, and a terminal event must arrive promptly — before the
// fix only the context fired, which the approver never observes, so the
// loop stayed blocked until the 60s approval timeout.
func TestServe_E2E_WSCancelInterruptsApprovalWait(t *testing.T) {
	llmSrv := approvalLLM(t)
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxPromptAll(t, store)
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	sid, token, _ := startApprovalWaitPrompt(t, conn)

	// Cancel over the WebSocket itself.
	payload, _ := json.Marshal(map[string]string{"type": "cancel", "session_id": sid, "auth_token": token})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send cancel: %v", err)
	}

	evt := readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "cancelled" })
	if evt["idle"] == true {
		t.Errorf("cancelled event reports idle=true while an approval wait was live: %v", evt)
	}

	term := readWSTerminal(t, conn)
	if term["type"] != "error" {
		t.Errorf("terminal event = %v, want error (the cancelled approval must abort the run)", term["type"])
	}
}

// TestServe_E2E_RESTCancelInterruptsApprovalWait cancels via
// POST /api/cancel while the loop blocks in the approval wait, and pins the
// honest response body: idle=false because a live prompt was found.
func TestServe_E2E_RESTCancelInterruptsApprovalWait(t *testing.T) {
	llmSrv := approvalLLM(t)
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxPromptAll(t, store)
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	sid, token, _ := startApprovalWaitPrompt(t, conn)

	req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/api/cancel?session_id="+sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Session-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/cancel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		SessionID string `json:"session_id"`
		Idle      bool   `json:"idle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode cancel body: %v", err)
	}
	if body.Idle || body.SessionID != sid {
		t.Errorf("cancel body = %+v, want idle=false session_id=%s (a prompt was live)", body, sid)
	}

	term := readWSTerminal(t, conn)
	if term["type"] != "error" {
		t.Errorf("terminal event = %v, want error (the cancelled approval must abort the run)", term["type"])
	}
}

// TestServe_E2E_RunCancelInterruptsApprovalWait cancels a headless run that
// is stuck in waiting_approval. The run must reach "cancelled" AND actually
// unwind (Error set by the post-run error event) well under the approval
// ceiling it requested — before the fix the deferred approver.Cancel only
// ran after handlePrompt unblocked, i.e. at the ceiling.
func TestServe_E2E_RunCancelInterruptsApprovalWait(t *testing.T) {
	llmSrv := approvalLLM(t)
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, func(rc *config.ResolvedConfig) {
		prompt := "prompt"
		rc.Dangerous.DefaultAction = &prompt
	})

	// Generous approval window: the cancel must beat it by a wide margin.
	_, resp := startTestRun(t, env, `{"content":"run a command","approval_timeout_seconds":600}`)
	runID, _ := resp["run_id"].(string)

	// Wait until the run is blocked in the approval wait.
	deadline := time.Now().Add(cancelTestTimeout)
	for {
		run := lookupRun(runID)
		if run == nil {
			t.Fatal("run vanished")
		}
		if s, _ := run.snapshot(false)["status"].(string); s == "waiting_approval" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never entered waiting_approval")
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancelStart := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/cancel", nil)
	w := httptest.NewRecorder()
	handleRunCancel()(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d (body: %s)", w.Code, w.Body.String())
	}

	// The Error field is only set once handlePrompt has RETURNED (the error
	// event flows through run.record), so polling for it proves the loop
	// unwound instead of merely relabeling the status.
	unwindDeadline := cancelStart.Add(cancelTestTimeout)
	for {
		run := lookupRun(runID)
		if run == nil {
			t.Fatal("run vanished")
		}
		snap := run.snapshot(false)
		status, _ := snap["status"].(string)
		errMsg, _ := snap["error"].(string)
		if status == "cancelled" && errMsg != "" {
			break // unwound and terminal
		}
		if time.Now().After(unwindDeadline) {
			t.Fatalf("run did not unwind within %v (status=%q error=%q) — approval wait survived the cancel",
				cancelTestTimeout, status, errMsg)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if elapsed := time.Since(cancelStart); elapsed > maxRunApprovalWait {
		t.Errorf("unwind took %v — slower than the approval ceiling it should preempt", elapsed)
	}

	// Cancelling again reports idle.
	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/cancel", nil)
	w = httptest.NewRecorder()
	handleRunCancel()(w, req)
	if !strings.Contains(w.Body.String(), `"idle":true`) {
		t.Errorf("second cancel body = %s", w.Body.String())
	}
}

// TestServe_E2E_WSCancelRunningPromptTerminalEvent cancels a prompt whose
// LLM call is still in flight (running, not approving). Pins that the
// cancelled reply is non-idle AND a terminal event reaches the client.
func TestServe_E2E_WSCancelRunningPromptTerminalEvent(t *testing.T) {
	block := make(chan struct{})
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		<-block // hold the chat completion open until the test ends
	}))
	defer llmSrv.Close()
	defer close(block)
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxV2(t, store, nil)
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()
	readWSUntil(t, conn, 10*time.Second, func(e map[string]any) bool { return e["type"] == "server_info" })

	payload, _ := json.Marshal(map[string]string{"type": "prompt", "content": "slow answer"})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	sessEvt := readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "session" })
	sid, _ := sessEvt["session_id"].(string)
	token, _ := sessEvt["auth_token"].(string)
	if sid == "" {
		t.Fatal("session event without id")
	}

	cancelPayload, _ := json.Marshal(map[string]string{"type": "cancel", "session_id": sid, "auth_token": token})
	if err := golangws.Message.Send(conn, string(cancelPayload)); err != nil {
		t.Fatalf("Send cancel: %v", err)
	}

	evt := readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "cancelled" })
	if evt["idle"] == true {
		t.Errorf("cancelled event reports idle=true while a prompt was running: %v", evt)
	}

	term := readWSTerminal(t, conn)
	if term["type"] != "error" {
		t.Errorf("terminal event = %v, want error (in-flight LLM call must abort with the context)", term["type"])
	}
}

// TestServe_E2E_ApprovalWorksAfterCancel pins the re-arm contract end to
// end: cancelling out of an approval wait must not poison the connection's
// approver — the NEXT prompt on the same socket can still be approved and
// complete. (A one-shot cancel channel would auto-deny every later
// approval with "approval cancelled".)
func TestServe_E2E_ApprovalWorksAfterCancel(t *testing.T) {
	llmSrv := approvalLLM(t)
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMuxPromptAll(t, store)
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	// Run 1: cancel out of its approval wait.
	sid, token, _ := startApprovalWaitPrompt(t, conn)
	payload, _ := json.Marshal(map[string]string{"type": "cancel", "session_id": sid, "auth_token": token})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send cancel: %v", err)
	}
	readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "cancelled" })
	readWSTerminal(t, conn)

	// Run 2 on the SAME connection: approval must still round-trip.
	llmSrv.Reset()
	payload, _ = json.Marshal(map[string]string{"type": "prompt", "content": "run it again", "session_id": sid, "auth_token": token})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send second prompt: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(cancelTestTimeout))
	var approved bool
	for i := 0; i < 100; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive run-2 event %d: %v", i, err)
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		switch evt["type"] {
		case "approval_request":
			id, _ := evt["id"].(string)
			resp, _ := json.Marshal(map[string]string{"type": "approval_response", "id": id, "action": "approve"})
			if err := golangws.Message.Send(conn, string(resp)); err != nil {
				t.Fatalf("Send approve: %v", err)
			}
			approved = true
		case "done":
			if !approved {
				t.Fatal("run 2 finished without ever prompting — approvals were poisoned by the earlier cancel")
			}
			return
		case "error":
			msg, _ := evt["message"].(string)
			if strings.Contains(msg, "cancelled") || strings.Contains(msg, "denied") {
				t.Fatalf("run 2 approval was auto-%s — the approver did not re-arm after the cancel", msg)
			}
			t.Fatalf("run 2 failed: %v", evt["message"])
		}
	}
	t.Fatal("run 2 never reached done")
}

// gatingResolver blocks inside Load until released or the load context is
// cancelled — parking handlePrompt in its @-ref resolution window, i.e.
// AFTER the early cancel registration but BEFORE the late one.
type gatingResolver struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatingResolver) Prefix() string { return "" }
func (g *gatingResolver) Search(context.Context, string, int) ([]resource.Resource, error) {
	return nil, nil
}
func (g *gatingResolver) Load(ctx context.Context, id string) (string, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return "gate content", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestServe_E2E_CancelDuringSetupWindowHonored closes the pre-registration
// window: a cancel arriving while handlePrompt is still setting up (here:
// blocked resolving an @-ref, before RunWithMessages) must find the early
// registration, report idle=false, and abort the prompt instead of letting
// it run to completion.
func TestServe_E2E_CancelDuringSetupWindowHonored(t *testing.T) {
	// LLM would finish the turn if the prompt ever got there — it must not.
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"should never be reached"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	sess, err := store.Create([]llm.Message{{Role: "user", Content: "prior"}}, "m", "setup window")
	if err != nil {
		t.Fatal(err)
	}

	gate := &gatingResolver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	resReg := resource.NewRegistry(gate)
	ln, mux := buildServeMuxWithResolvers(t, store, resReg)
	defer ln.Close()
	defer close(gate.release)
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	payload, _ := json.Marshal(map[string]string{
		"type":       "prompt",
		"content":    "summarize @gate.txt",
		"session_id": sess.ID,
		"auth_token": sess.AuthToken,
	})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Deterministic barrier: once Load is entered, the early registration
	// has already happened (it precedes ref resolution in handlePrompt).
	select {
	case <-gate.entered:
	case <-time.After(cancelTestTimeout):
		t.Fatal("@-ref resolution never started")
	}

	req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/api/cancel?session_id="+sess.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Session-Token", sess.AuthToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/cancel: %v", err)
	}
	var body struct {
		SessionID string `json:"session_id"`
		Idle      bool   `json:"idle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode cancel body: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", resp.StatusCode)
	}
	if body.Idle {
		t.Errorf("cancel during the setup window reported idle=true — the pre-registration window dropped it (body: %+v)", body)
	}

	term := readWSTerminal(t, conn)
	if term["type"] != "error" {
		t.Errorf("terminal event = %v, want error (a cancelled setup must not run the prompt)", term["type"])
	}
}
