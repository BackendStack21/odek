package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/resource"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// A REST approval answer that lands after the approval already expired must
// not be acknowledged: the waiter has taken the timeout branch, so the
// command is denied. PromptCommand announces the expiry (approval_expired)
// while the pending entry is still registered — the entry is only removed by
// a deferred delete after the timeout error is built — so HandleResponse
// still finds the id, delivers into the abandoned buffered channel and
// reports success. The REST bridge then answers 200 {"action":"approve"} and
// records an approval_ack for a command that never ran.
func TestRED_RESTApprovalAnswerAckedAfterExpiry(t *testing.T) {
	run := &serveRun{ID: "run-hunt12b-expiry", Status: "running", pending: map[string]*approvalRequest{}}
	run.cond = sync.NewCond(&run.mu)
	var (
		lateCode int
		lateBody string
		answered = make(chan struct{})
	)
	var approver *wsApprover
	approver = newWSApprover(func(v any) error {
		if ar, ok := v.(approvalRequest); ok {
			run.recordApprovalRequest(ar)
			return nil
		}
		if m, ok := v.(map[string]any); ok && m["type"] == "approval_expired" {
			// The operator's answer arrives right as the wait expires.
			id, _ := m["id"].(string)
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/approvals/"+id, strings.NewReader(`{"action":"approve"}`))
			rec := httptest.NewRecorder()
			handleRunApprovalAnswer()(rec, req)
			lateCode, lateBody = rec.Code, rec.Body.String()
			close(answered)
		}
		return run.record(v)
	})
	approver.SetApprovalTimeout(20 * time.Millisecond)
	run.approver = approver
	registerRun(run)
	t.Cleanup(resetServeRuns)

	err := approver.PromptCommand(danger.SystemWrite, "touch /etc/odek-hunt12b", "")
	<-answered
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("PromptCommand = %v, want approval timeout", err)
	}
	if lateCode == http.StatusOK {
		t.Fatalf("late answer for an expired approval was acknowledged: %d %s — the command was denied by timeout", lateCode, lateBody)
	}
}

// serveHunt12bLLMServer answers with a shell tool call when the request
// carries the marker "run-the-tool" and no tool result yet; every other chat
// call gets a plain final answer. Content-keyed (not call-count-keyed) so
// side calls cannot shift the script.
func serveHunt12bLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "run-the-tool") && !strings.Contains(string(body), `"role":"tool"`) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Running.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo hunt12b\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
}

// docs/SECURITY.md "Audit log": every wrapper entering the user message
// (@-references, attachments) and every untrusted tool result is recorded,
// and after each turn the divergence heuristic runs. In serve the ingest
// recorder and the turn audit are keyed on auditSessID, which is computed
// BEFORE a new conversation's session is created — so the first turn of
// every new WebUI / REST conversation records nothing at all: no ingest for
// its @-references, attachments or tool results, and no turn assessment.
func TestRED_ServeFirstTurnOfNewSessionIsNeverAudited(t *testing.T) {
	llm := serveHunt12bLLMServer(t)
	defer llm.Close()
	envCleanup := setTestEnv(t, llm.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("quarterly numbers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, mux := buildServeMuxWithResolvers(t, store, resource.NewRegistry(resource.NewFileResolver(work)))
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	if err := golangws.Message.Send(conn, `{"type":"prompt","content":"summarize @notes.txt"}`); err != nil {
		t.Fatal(err)
	}
	var sid string
	readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool {
		if e["type"] == "session" {
			sid, _ = e["session_id"].(string)
		}
		if e["type"] == "error" {
			t.Fatalf("turn failed: %v", e["message"])
		}
		return e["type"] == "done"
	})
	if sid == "" {
		t.Fatal("no session id")
	}
	log, err := session.NewAuditStore(store.Dir()).Load(sid)
	if err != nil {
		t.Fatalf("audit load: %v", err)
	}
	if len(log.Ingests) == 0 || len(log.Turns) == 0 {
		t.Fatalf("first turn of new session %s left no audit trail (ingests=%d turns=%d) although it inlined @notes.txt", sid, len(log.Ingests), len(log.Turns))
	}
}

// Deleting a session must not leave its audit log behind ("it must not
// outlive a session the operator deleted", Store.removeFilesLocked). When the
// session is deleted while a turn is running, Delete removes the audit file
// and cancels the prompt, but the unwinding handlePrompt still calls
// recordTurnAudit for the deleted id, which re-creates
// <sessions>/audit/<id>.json holding the user's prompt.
func TestRED_ServeDeleteMidTurnResurrectsAuditLog(t *testing.T) {
	llm := serveHunt12bLLMServer(t)
	defer llm.Close()
	envCleanup := setTestEnv(t, llm.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	wireServeSessionCleanup(store, nil, t.TempDir()) // production OnDelete: cancels the live prompt
	ln, mux := buildServeMuxPromptAll(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	// Turn 1 creates the session.
	if err := golangws.Message.Send(conn, `{"type":"prompt","content":"hello"}`); err != nil {
		t.Fatal(err)
	}
	var sid, tok string
	readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool {
		if e["type"] == "session" {
			sid, _ = e["session_id"].(string)
			tok, _ = e["auth_token"].(string)
		}
		return e["type"] == "done"
	})
	// Turn 2 blocks in an approval wait.
	payload, _ := json.Marshal(map[string]string{"type": "prompt", "content": "run-the-tool secret-prompt-text", "session_id": sid, "auth_token": tok})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatal(err)
	}
	readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "approval_request" })

	if err := store.Delete(sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	auditPath := filepath.Join(store.Dir(), "audit", sid+".json")
	if _, err := os.Stat(auditPath); err == nil {
		t.Fatal("precondition: Delete did not remove the audit log")
	}
	readWSUntil(t, conn, cancelTestTimeout, func(e map[string]any) bool { return e["type"] == "turn_settled" })

	if data, err := os.ReadFile(auditPath); err == nil {
		t.Fatalf("audit log of deleted session %s was re-created by the unwinding turn (%d bytes):\n%s", sid, len(data), data)
	}
}

// The 404-vs-401 session lookup oracle was closed for every HTTP
// session-scoped endpoint by routing rejections through
// sessionLookupLimiter, but the WebSocket transport answers the same lookups
// with no budget at all: cancel, subagent_cancel, session_switch and prompt
// frames report "session not found" vs "invalid session token" for every
// guess, so one socket probes ids/tokens without ever being throttled.
func TestRED_WSSessionLookupRejectionsAreRateLimited(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusInternalServerError)
	}))
	defer llm.Close()
	envCleanup := setTestEnv(t, llm.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test", "victim")
	if err != nil {
		t.Fatal(err)
	}
	ln, mux := buildServeMuxWithResolvers(t, store, resource.NewRegistry())
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	sessionLookupLimiter.reset()
	t.Cleanup(sessionLookupLimiter.reset)
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	limited := false
	for i := 0; i < 80 && !limited; i++ {
		frame := `{"type":"cancel","session_id":"` + sess.ID + `","auth_token":"guess-` + strings.Repeat("x", i%5) + `"}`
		if err := golangws.Message.Send(conn, frame); err != nil {
			t.Fatalf("send: %v", err)
		}
		evt := readWSUntil(t, conn, 5*time.Second, func(e map[string]any) bool {
			return e["type"] == "error" || e["type"] == "cancelled"
		})
		msg, _ := evt["message"].(string)
		if strings.Contains(msg, "rate limit") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("80 bad-token WebSocket cancel frames for one session were never rate limited (unthrottled 404/401 oracle)")
	}
}

// The prompt-time session and referenced-session gates charge the peer's
// lookup budget when the prompt context carries a client address.
func TestRED_PromptLookupRejectionsShareTheLookupBudget(t *testing.T) {
	sessionLookupLimiter.reset()
	t.Cleanup(sessionLookupLimiter.reset)
	ctx := context.WithValue(context.Background(), serveClientIPKey{}, "203.0.113.9")
	limited := false
	for i := 0; i < 80; i++ {
		if ctxLookupRejectMsg(ctx, "invalid token for referenced session") == "rate limit exceeded" {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("prompt-time rejections were never rate limited")
	}
	if got := ctxLookupRejectMsg(context.Background(), "session not found"); got != "session not found" {
		t.Fatalf("context without a client address must not be limited, got %q", got)
	}
}

// Ingests held before a new session exists are bounded by bytes; the excess
// is dropped and reported in a marker ingest.
func TestRED_ServeAuditBinderPendingIsByteBounded(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	b := &serveAuditBinder{store: session.NewAuditStore(store.Dir()).RequireSessionFile()}
	chunk := strings.Repeat("a", 1<<20)
	for i := 0; i < 8; i++ {
		b.ingest("resource:big", chunk+string(rune('a'+i)))
	}
	if b.pendingB > serveAuditPendingMaxBytes {
		t.Fatalf("pending holds %d bytes, cap %d", b.pendingB, serveAuditPendingMaxBytes)
	}
	b.bind(sess.ID, 1)
	log, err := session.NewAuditStore(store.Dir()).Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	marker := false
	for _, in := range log.Ingests {
		if strings.HasPrefix(in.Source, "audit:dropped_ingests:") {
			marker = true
		}
	}
	if !marker {
		t.Fatalf("no drop marker among %d ingests", len(log.Ingests))
	}
}

// Whenever an answer was accepted (HandleResponse / HandleClarifyResponse
// returned true) the waiter must act on it, even when it lands in the same
// instant as the timeout: a denial is a denial, an answer is the answer.
func TestRED_AnswerAcceptedAtTimeoutIsHonored(t *testing.T) {
	violations := 0
	for i := 0; i < 400; i++ {
		idCh := make(chan string, 1)
		a := newWSApprover(func(v any) error {
			if ar, ok := v.(approvalRequest); ok {
				idCh <- ar.ID
			}
			return nil
		})
		a.SetApprovalTimeout(time.Millisecond)
		accepted := make(chan bool, 1)
		go func() {
			id := <-idCh
			time.Sleep(time.Millisecond - time.Duration(i%5)*100*time.Microsecond)
			accepted <- a.HandleResponse(id, "deny")
		}()
		err := a.PromptCommand(danger.SystemWrite, "touch /etc/x", "")
		if ok := <-accepted; ok && (err == nil || !strings.Contains(err.Error(), "denied")) {
			violations++
		}
	}
	if violations > 0 {
		t.Fatalf("%d accepted denials were reported as timeouts", violations)
	}
}

func TestRED_ClarifyAnswerAcceptedAtTimeoutIsHonored(t *testing.T) {
	old := clarifyTimeout
	clarifyTimeout = time.Millisecond
	defer func() { clarifyTimeout = old }()
	violations := 0
	for i := 0; i < 400; i++ {
		idCh := make(chan string, 1)
		a := newWSApprover(func(v any) error {
			if cr, ok := v.(clarifyRequest); ok {
				idCh <- cr.ID
			}
			return nil
		})
		accepted := make(chan bool, 1)
		go func() {
			id := <-idCh
			time.Sleep(time.Millisecond - time.Duration(i%5)*100*time.Microsecond)
			accepted <- a.HandleClarifyResponse(id, "yes")
		}()
		ans, err := a.PromptClarify("q?")
		if ok := <-accepted; ok && (err != nil || ans != "yes") {
			violations++
		}
	}
	if violations > 0 {
		t.Fatalf("%d accepted clarify answers were lost to the timeout", violations)
	}
}

// Headless runs charge their @sess: rejections to the caller's lookup budget
// (the address handlePromptStart saw), like the WebSocket transport.
func TestRED_RunReferenceRejectionsAreRateLimited(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	defer llm.Close()
	defer setTestEnv(t, llm.URL)()
	sessionLookupLimiter.reset()
	t.Cleanup(sessionLookupLimiter.reset)
	const ip = "203.0.113.77"
	for i := 0; i < 60; i++ {
		sessionLookupLimiter.allow(ip) // spend the budget
	}
	run, err := startServeRun(loadJSONMockResolved(), "Help", newTestSessionStore(t), nil, promptRequest{
		Content:  "look at @sess:20260101-00000000000000000000000000000000",
		clientIP: ip,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		snap, _ := json.Marshal(run.snapshot(true))
		if strings.Contains(string(snap), "rate limit exceeded") {
			return
		}
		if strings.Contains(string(snap), "referenced session not found") {
			t.Fatalf("rejection inside a headless run was not rate limited: %s", snap)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("run never reported the reference rejection")
}
