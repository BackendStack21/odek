package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/BackendStack21/odek/internal/session"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	golangws "golang.org/x/net/websocket"
)

// wsHuntReadTurn receives frames until done/error and returns every frame.
func wsHuntReadTurn(t *testing.T, conn *golangws.Conn) []map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var out []map[string]any
	for i := 0; i < 300; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("receive frame %d: %v", i, err)
		}
		var evt map[string]any
		if json.Unmarshal(raw, &evt) != nil {
			continue
		}
		out = append(out, evt)
		if evt["type"] == "turn_settled" {
			return out
		}
	}
	t.Fatal("no turn_settled frame")
	return nil
}

func wsHuntSessionID(frames []map[string]any) string {
	for _, f := range frames {
		if f["type"] == "session" {
			s, _ := f["session_id"].(string)
			return s
		}
	}
	return ""
}

// The Web UI "New session" button clears S.sessionId and the next prompt goes
// out with "session_id": null — exactly the documented "omit for new session"
// wire contract (docs/WEBUI.md, Client → Server). The server must start a new
// conversation; instead it silently continues the connection's previous one.
func TestRED_WSPromptWithoutSessionIDAfterTurnStartsNewSession(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()
	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	send := func(content string) []map[string]any {
		payload, _ := json.Marshal(map[string]any{"type": "prompt", "content": content, "session_id": nil})
		if err := golangws.Message.Send(conn, string(payload)); err != nil {
			t.Fatalf("send: %v", err)
		}
		return wsHuntReadTurn(t, conn)
	}
	first := wsHuntSessionID(send("first conversation"))
	second := wsHuntSessionID(send("brand new conversation"))
	if first == "" || second == "" {
		t.Fatalf("missing session frames: first=%q second=%q", first, second)
	}
	if first == second {
		sess, _ := store.Load(first)
		n := 0
		for _, m := range sess.Messages {
			if m.Role == "user" && strings.Contains(m.Content, "brand new conversation") {
				n++
			}
		}
		t.Fatalf("prompt with session_id=null continued the previous session %s (new prompt appended to it: %d) instead of creating a new one", first, n)
	}
}

// wsUIFuncReadSessionUntilDone receives WS frames until "done" and returns the
// session id announced by the turn's session frame.
func wsUIFuncReadSessionUntilDone(t *testing.T, conn *golangws.Conn) string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	sid := ""
	for i := 0; i < 200; i++ {
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
			sid, _ = evt["session_id"].(string)
		case "done":
			return sid
		case "error":
			t.Fatalf("unexpected error event: %s", string(raw))
		}
	}
	t.Fatal("no done event received")
	return ""
}

// The WebUI "New session" button clears S.sessionId and sends the next
// prompt without session_id (docs/WEBUI.md: "optional — omit for new
// session"). The server substitutes the connection's previous session, so the
// "new" conversation is appended to the old one.
func TestRED_UIFuncPromptWithoutSessionIDContinuesPreviousSession(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	send := func(content string) string {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"type": "prompt", "content": content})
		if err := golangws.Message.Send(conn, string(payload)); err != nil {
			t.Fatalf("Send: %v", err)
		}
		return wsUIFuncReadSessionUntilDone(t, conn)
	}

	first := send("first conversation")
	second := send("brand new conversation")
	if first == "" || second == "" {
		t.Fatalf("missing session frames: %q %q", first, second)
	}
	if first == second {
		turns := 0
		if sess, err := store.Load(first); err == nil {
			turns = sess.Turns
		}
		t.Fatalf("prompt without session_id was appended to the previous session %s (turns=%d); docs say omitting session_id starts a new session", first, turns)
	}
}

// wsFixConnectionsBoundTo counts registered WebSocket connections whose live
// session binding is sid.
func wsFixConnectionsBoundTo(t *testing.T, sid string) int {
	t.Helper()
	w := httptest.NewRecorder()
	handleConnections()(w, httptest.NewRequest(http.MethodGet, "/api/connections", nil))
	var body struct {
		Connections []struct {
			SessionID string `json:"session_id"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range body.Connections {
		if c.SessionID == sid {
			n++
		}
	}
	return n
}

// The Web UI "New session" control sends session_new: the connection must
// stop being bound to the previous session (background wakes for it no
// longer target this socket) before any new prompt arrives, and the
// previous session must still resume by id afterwards.
func TestRED_WSSessionNewFrameDetachesConnection(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()
	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	payload, _ := json.Marshal(map[string]any{"type": "prompt", "content": "first conversation"})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatal(err)
	}
	frames := wsHuntReadTurn(t, conn)
	first := wsHuntSessionID(frames)
	token := ""
	for _, f := range frames {
		if f["type"] == "session" {
			token, _ = f["auth_token"].(string)
		}
	}
	if first == "" || wsFixConnectionsBoundTo(t, first) != 1 {
		t.Fatalf("precondition: connection not bound to %q", first)
	}
	if err := golangws.Message.Send(conn, `{"type":"session_new"}`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for wsFixConnectionsBoundTo(t, first) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("session_new left the connection bound to the previous session %s", first)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Resuming the previous session by id still works.
	payload, _ = json.Marshal(map[string]any{"type": "prompt", "content": "back again", "session_id": first, "auth_token": token})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatal(err)
	}
	if got := wsHuntSessionID(wsHuntReadTurn(t, conn)); got != first {
		t.Fatalf("resume by id landed in %q, want %q", got, first)
	}
	sess, err := store.Load(first)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Turns != 2 {
		t.Fatalf("resumed session turns = %d, want 2", sess.Turns)
	}
}

// Renaming (or pinning) a session from the sidebar while its WebUI turn runs
// bumps the stored revision; the turn's next per-step checkpoint then fails
// with a revision conflict and the running turn is aborted ("failed to
// persist session"). A metadata edit must not kill a live turn.
func TestRED_WSRenameDuringTurnDoesNotAbortTurn(t *testing.T) {
	release := make(chan struct{})
	reached := make(chan struct{}, 1)
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"math_eval","arguments":"{\"expression\":\"1+1\"}"}}]}}]}`))
			return
		}
		if callCount == 2 {
			reached <- struct{}{}
			<-release
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"final answer"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()
	store := newTestSessionStore(t)
	sess, err := store.Create([]session.Message{{Role: "system", Content: ""}}, "test-model", "orig")
	if err != nil {
		t.Fatal(err)
	}
	ln, mux := buildServeMuxWithSessionByID(t, store)
	defer startServeTest(t, ln, mux)()
	addr := ln.Addr().String()
	waitForHTTP(t, addr)
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, addr)
	defer conn.Close()

	payload, _ := json.Marshal(map[string]any{"type": "prompt", "content": "compute", "session_id": sess.ID, "auth_token": sess.AuthToken})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(15 * time.Second):
		close(release)
		t.Fatal("second LLM call never reached")
	}
	// Rename through the production REST route while the turn is mid-flight.
	testTokenMu.Lock()
	tok := testLastToken
	testTokenMu.Unlock()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/sessions/"+sess.ID, strings.NewReader(`{"name":"renamed"}`))
	req.Header.Set("X-Odek-Ws-Token", tok)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	req.Header.Set("Origin", "http://"+addr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	resp.Body.Close()
	close(release)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status %d", resp.StatusCode)
	}
	frames := wsHuntReadTurn(t, conn)
	var errs []string
	done := false
	for _, f := range frames {
		switch f["type"] {
		case "error":
			m, _ := f["message"].(string)
			errs = append(errs, m)
		case "done":
			done = true
		}
	}
	if !done || len(errs) > 0 {
		t.Fatalf("turn aborted after a mid-turn rename: done=%v errors=%q", done, errs)
	}
	stored, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Task != "renamed" {
		t.Fatalf("rename lost after the turn saved: task = %q", stored.Task)
	}
	if last := stored.Messages[len(stored.Messages)-1]; last.Content != "final answer" {
		t.Fatalf("persisted transcript ends with %q, want the turn's final answer", last.Content)
	}
}

// The WebUI sidebar exposes pin and rename on every session row at any time,
// including the session whose turn is running. The REST handler loads and
// Saves the session without the execution lock, bumping its revision; the
// running turn's next per-step persist then hits ErrConflict, which cancels
// the run ("failed to persist session") and loses the rest of the turn.
func TestRED_UIFuncPinOrRenameDuringRunAbortsTheTurn(t *testing.T) {
	release := make(chan struct{})
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"role":"tool"`)) {
			w.Write([]byte(`{"choices":[{"message":{"content":"final-answer-ok"},"finish_reason":"stop"}]}`))
			return
		}
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"calc","type":"function","function":{"name":"math_eval","arguments":"{\"expression\":\"1+1\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer llm.Close()
	envCleanup := setTestEnv(t, llm.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	payload, _ := json.Marshal(map[string]any{"type": "prompt", "content": "compute something"})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	sid, token := "", ""
	released := false
	var final map[string]any
	for i := 0; i < 400 && final == nil; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		var evt map[string]any
		if json.Unmarshal(raw, &evt) != nil {
			continue
		}
		switch evt["type"] {
		case "session":
			sid, _ = evt["session_id"].(string)
			token, _ = evt["auth_token"].(string)
			if !released {
				// Operator pins the running session from the sidebar.
				// The pin races the turn's pre-run prompt save and must
				// still succeed on the first attempt.
				req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid, strings.NewReader(`{"pinned":true}`))
				req.Header.Set("X-Session-Token", token)
				rec := httptest.NewRecorder()
				handleSessionByID(store, nil, "")(rec, req)
				if rec.Code != http.StatusOK {
					close(release)
					t.Fatalf("pin racing the pre-run save answered %d: %s", rec.Code, rec.Body.String())
				}
				close(release)
				released = true
			}
		case "done", "error":
			final = evt
		}
	}
	if !released {
		close(release)
	}
	if final == nil {
		t.Fatal("turn never finished")
	}
	if final["type"] == "error" {
		t.Fatalf("pinning the running session aborted its turn: %v", final["message"])
	}
	sess, err := store.Load(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.Pinned {
		t.Fatalf("pin lost after the turn saved")
	}
}

// wsAPIPostSession issues POST /api/sessions/{id} through the
// production handler with the session token.
func wsAPIPostSession(t *testing.T, store *session.Store, id, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id, strings.NewReader(body))
	req.Header.Set("X-Session-Token", token)
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(w, req)
	return w
}

// TestRED_SessionPinDuringLiveTurnAbortsTurn: pinning (or renaming) a
// session from the sidebar while its turn is running bumps the stored
// revision; the live turn's next per-step checkpoint then fails with
// "stale revision", the run is cancelled and its progress is lost.
func TestRED_SessionPinDuringLiveTurnAbortsTurn(t *testing.T) {
	inSecond := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		switch callCount {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c_1","type":"function","function":{"name":"math_eval","arguments":"{\"expression\":\"1+1\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		case 2:
			once.Do(func() { close(inSecond) })
			<-release
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c_2","type":"function","function":{"name":"math_eval","arguments":"{\"expression\":\"2+2\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"final answer"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		}
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	sess, err := env.store.Create([]session.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "ok"}}, "m", "pin me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"content": "compute things", "session_id": sess.ID, "auth_token": sess.AuthToken})
	_, resp := startTestRun(t, env, string(body))
	runID, _ := resp["run_id"].(string)

	select {
	case <-inSecond:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("run never reached the second LLM call")
	}
	w := wsAPIPostSession(t, env.store, sess.ID, sess.AuthToken, `{"pinned":true}`)
	close(release)
	if w.Code != http.StatusOK {
		t.Fatalf("pin answered %d (%s)", w.Code, strings.TrimSpace(w.Body.String()))
	}

	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "completed" {
		t.Fatalf("run status = %v (error: %v), want completed: a pin from the session list must not abort the live turn", snap["status"], snap["error"])
	}
	final, err := env.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := final.Messages[len(final.Messages)-1]
	if last.Content != "final answer" {
		t.Fatalf("persisted transcript ends with %q, want the turn's final answer", last.Content)
	}
	if !final.Pinned {
		t.Fatal("pin lost after the turn's final save")
	}
}

// The WebUI question card caps answers with textarea maxLength=32000, which
// counts UTF-16 code units; HandleClarifyResponse caps at 32000 BYTES and
// drops anything longer silently (no NACK, no clarify_ack). A non-ASCII answer
// the card accepts (here 12000 CJK characters = 36000 bytes) is discarded and
// the card stays "Sending answer… waiting for server acceptance" with every
// button disabled until the 300s expiry.
func TestRED_WSClarifyAnswerWithinUILimitIsDelivered(t *testing.T) {
	a := newWSApprover(nil)
	reqs := make(chan clarifyRequest, 1)
	a.sendFn = func(v any) error {
		if f, ok := v.(clarifyRequest); ok {
			reqs <- f
		}
		return nil
	}
	answer := strings.Repeat("日", 12000) // 12000 UTF-16 units (< UI 32000), 36000 bytes
	type result struct {
		ans string
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := a.PromptClarify("which?")
		done <- result{s, err}
	}()
	req := <-reqs
	delivered := a.HandleClarifyResponse(req.ID, answer)
	if !delivered {
		// Unblock the wait; the failure below reports the drop.
		a.Cancel()
	}
	r := <-done
	if !delivered || r.err != nil || r.ans != answer {
		t.Fatalf("answer accepted by the UI card (12000 chars) was dropped by the server: delivered=%v err=%v (no ack/nack frame is sent, card hangs until expiry)", delivered, r.err)
	}
}

// An answer the server refuses for a pending question (over the character
// cap, or blank) is answered with clarify_rejected carrying the id, the
// question stays pending, and a corrected answer is still delivered.
func TestRED_WSClarifyRejectedAnswerIsNacked(t *testing.T) {
	a := newWSApprover(nil)
	reqs := make(chan clarifyRequest, 1)
	frames := make(chan map[string]any, 8)
	a.sendFn = func(v any) error {
		switch f := v.(type) {
		case clarifyRequest:
			reqs <- f
		case map[string]any:
			frames <- f
		}
		return nil
	}
	type result struct {
		ans string
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := a.PromptClarify("which?")
		done <- result{s, err}
	}()
	req := <-reqs
	for _, bad := range []string{strings.Repeat("日", maxClarifyAnswerChars+1), strings.Repeat("a", maxClarifyAnswerChars+1), "   "} {
		if a.HandleClarifyResponse(req.ID, bad) {
			t.Fatalf("refused answer (%d bytes) reported delivered", len(bad))
		}
		select {
		case f := <-frames:
			if f["type"] != "clarify_rejected" || f["id"] != req.ID || f["reason"] == "" {
				t.Fatalf("NACK frame = %v", f)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no clarify_rejected frame for a refused answer")
		}
	}
	// Exactly at the cap in characters (multi-byte) is accepted.
	ok := strings.Repeat("日", maxClarifyAnswerChars)
	if !a.HandleClarifyResponse(req.ID, ok) {
		t.Fatal("answer at the character cap refused")
	}
	r := <-done
	if r.err != nil || r.ans != ok {
		t.Fatalf("corrected answer not delivered: err=%v", r.err)
	}
	// No NACK for an id that is not pending (unknown or already answered).
	a.HandleClarifyResponse("clr-unknown", "")
	select {
	case f := <-frames:
		if f["type"] == "clarify_rejected" {
			t.Fatalf("NACK sent for a question that is not pending: %v", f)
		}
	default:
	}
}

// docs/WEBUI.md (and the composer) allow text attachments of 5 MiB each and
// 10 MiB in total, sent inline in the prompt frame. The socket's
// MaxPayloadBytes is 8 MiB, so a prompt with two 4.5 MiB files (9 MiB, inside
// every documented cap) makes Receive fail with ErrFrameTooLarge, which the
// reader treats as a disconnect: the socket is torn down with no error frame
// and the prompt silently vanishes (the UI only sees a reconnect).
func TestRED_WSDocumentedAttachmentSizeDoesNotDropConnection(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()
	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	// The whole documented budget: a 1 MiB prompt and two 5 MiB files
	// (10 MiB total), with newlines that JSON escaping doubles.
	file := strings.Repeat("line of text\n", 5*1024*1024/13)
	payload, _ := json.Marshal(map[string]any{
		"type":    "prompt",
		"content": "summarise the files " + strings.Repeat("p", maxPromptBytes-32),
		"attachments": []map[string]string{
			{"name": "a.txt", "content": file},
			{"name": "b.txt", "content": file},
		},
	})
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("server tore the socket down while a prompt within the documented attachment caps was being sent: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	for i := 0; i < 300; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("server dropped the connection on a prompt within the documented attachment caps (no error frame): %v", err)
		}
		var evt map[string]any
		if json.Unmarshal(raw, &evt) != nil {
			continue
		}
		if evt["type"] == "error" {
			t.Fatalf("prompt within the documented caps was refused: %v", evt["message"])
		}
		if evt["type"] == "turn_started" { // accepted past every size check
			return
		}
	}
	t.Fatal("prompt never started")
}

// A message over the frame cap is not buffered; the server answers it with an
// error frame and keeps the socket usable, and closes a connection that keeps
// sending them.
func TestRED_WSOversizedFrameGetsErrorFrame(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()
	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	waitForHTTP(t, ln.Addr().String())
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	huge := `{"type":"prompt","content":"` + strings.Repeat("x", maxWSMessageBytes+1024) + `"}`
	readUntil := func(want func(map[string]any) bool) {
		t.Helper()
		for {
			var raw []byte
			if err := golangws.Message.Receive(conn, &raw); err != nil {
				t.Fatalf("connection dropped: %v", err)
			}
			var evt map[string]any
			if json.Unmarshal(raw, &evt) == nil && want(evt) {
				return
			}
		}
	}
	tooLarge := func(e map[string]any) bool {
		m, _ := e["message"].(string)
		return e["type"] == "error" && strings.Contains(m, "too large")
	}
	for i := 1; i < maxWSOversizedFrames; i++ {
		if err := golangws.Message.Send(conn, huge); err != nil {
			t.Fatalf("oversized message %d: socket torn down: %v", i, err)
		}
		readUntil(tooLarge)
		// The socket still works after the oversized frame was skipped.
		if err := golangws.Message.Send(conn, `{"type":"ping"}`); err != nil {
			t.Fatal(err)
		}
		readUntil(func(e map[string]any) bool { return e["type"] == "pong" })
	}
	// One more closes the connection.
	if err := golangws.Message.Send(conn, huge); err != nil {
		return
	}
	for {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			return
		}
		if strings.Contains(string(raw), "too large") {
			t.Fatalf("connection still open after %d oversized messages", maxWSOversizedFrames)
		}
	}
}
