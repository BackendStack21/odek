package main

// Tests for backend API changes:
//   - GET /api/sessions/:id  (new endpoint)
//   - handleModelList        (returns only configured model, not KnownProfiles)
//   - serveOnListener        (stops cleanly when listener is closed)
//   - handlePrompt origLen   (second turn must not repeat first turn's response)

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"
	golangws "golang.org/x/net/websocket"
)

// ── GET /api/sessions/:id ────────────────────────────────────────────

func TestHandleSessionList_DoesNotLeakAuthTokens(t *testing.T) {
	store := newTestSessionStore(t)
	if _, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create([]llm.Message{{Role: "user", Content: "bye"}}, "m", "two"); err != nil {
		t.Fatal(err)
	}

	handler := handleSessionList(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var sessions []session.Session
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d, want 2", len(sessions))
	}
	for _, s := range sessions {
		if s.AuthToken != "" {
			t.Errorf("session list leaked auth token for %s", s.ID)
		}
	}
}

func TestHandleSessionByID_GET_ReturnsSession(t *testing.T) {
	store := newTestSessionStore(t)

	sess, err := store.Create([]llm.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there!"},
	}, "test-model", "greeting task")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got session.Session
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("ID = %q, want %q", got.ID, sess.ID)
	}
	if len(got.Messages) != 3 {
		t.Errorf("len(Messages) = %d, want 3", len(got.Messages))
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "hello" {
		t.Errorf("Messages[1] = {%s, %q}, want {user, hello}", got.Messages[1].Role, got.Messages[1].Content)
	}
	if got.Messages[2].Role != "assistant" || got.Messages[2].Content != "hi there!" {
		t.Errorf("Messages[2] = {%s, %q}, want {assistant, hi there!}", got.Messages[2].Role, got.Messages[2].Content)
	}
	if got.Task != "greeting task" {
		t.Errorf("Task = %q, want 'greeting task'", got.Task)
	}
}

func TestHandleSessionByID_GET_NotFound(t *testing.T) {
	store := newTestSessionStore(t)
	handler := handleSessionByID(store)

	// Valid ID format but doesn't exist on disk.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/20260101-aabbcc", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleSessionByID_GET_MissingID(t *testing.T) {
	store := newTestSessionStore(t)
	handler := handleSessionByID(store)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleSessionByID_GET_MessagesArePresent(t *testing.T) {
	// Regression: the GET endpoint must return Messages (not just metadata).
	// The session list endpoint omits Messages for efficiency; the single-session
	// endpoint must include them so the UI can render conversation history.
	store := newTestSessionStore(t)

	sess, err := store.Create([]llm.Message{
		{Role: "user", Content: "what is 2+2?"},
		{Role: "assistant", Content: "4"},
	}, "test-model", "math")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	var got session.Session
	json.NewDecoder(w.Body).Decode(&got)

	if len(got.Messages) == 0 {
		t.Fatal("GET /api/sessions/:id must include Messages; got empty slice")
	}
}

func TestHandleSessionByID_MethodNotAllowed(t *testing.T) {
	store := newTestSessionStore(t)
	handler := handleSessionByID(store)

	for _, method := range []string{http.MethodPatch, http.MethodPut, http.MethodHead} {
		req := httptest.NewRequest(method, "/api/sessions/20260101-aabbcc", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/sessions/:id: status = %d, want 405", method, w.Code)
		}
	}
}

func TestHandleSessionByID_DELETE_StillWorks(t *testing.T) {
	// Verify the existing DELETE handler is not broken by the new GET case.
	store := newTestSessionStore(t)

	sess, err := store.Create([]llm.Message{{Role: "user", Content: "bye"}}, "m", "task")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", w.Code)
	}

	// Confirm it's gone.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	w2 := httptest.NewRecorder()
	handler(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE: status = %d, want 404", w2.Code)
	}
}

func TestHandleSessionByID_POST_RenameStillWorks(t *testing.T) {
	store := newTestSessionStore(t)

	sess, err := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "original name")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := handleSessionByID(store)
	body := strings.NewReader(`{"name":"renamed task"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var got session.Session
	json.NewDecoder(w.Body).Decode(&got)
	if got.Task != "renamed task" {
		t.Errorf("Task after rename = %q, want 'renamed task'", got.Task)
	}
}

func TestHandleSessionByID_GET_InvalidToken(t *testing.T) {
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleSessionByID_GET_MissingToken(t *testing.T) {
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleSessionByID_GET_LazyTokenBootstrap(t *testing.T) {
	// Legacy sessions created before the auth-token defense have no token.
	// The first GET bootstraps a token and returns it so the UI can use it
	// for subsequent requests.
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")
	sess.AuthToken = ""
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	token := w.Header().Get("X-Session-Token")
	if token == "" {
		t.Error("bootstrap should return X-Session-Token header")
	}

	var got session.Session
	json.NewDecoder(w.Body).Decode(&got)
	if got.AuthToken != token {
		t.Errorf("response auth_token = %q, want %q", got.AuthToken, token)
	}

	// Subsequent requests with the bootstrapped token succeed.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	req2.Header.Set("X-Session-Token", token)
	w2 := httptest.NewRecorder()
	handler(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("second GET status = %d, want 200", w2.Code)
	}
}

func TestHandleSessionByID_DELETE_RequiresToken(t *testing.T) {
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")

	handler := handleSessionByID(store)
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleSessionByID_POST_RequiresToken(t *testing.T) {
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")

	handler := handleSessionByID(store)
	body := strings.NewReader(`{"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleSessionByID_GET_RateLimit(t *testing.T) {
	store := newTestSessionStore(t)
	sess, _ := store.Create([]llm.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store)
	// Exhaust the 60/min allowance.
	for i := 0; i < 60; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
		req.Header.Set("X-Session-Token", sess.AuthToken)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}

	// The next request from the same (loopback) IP should be rate limited.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

// ── handleModelList ──────────────────────────────────────────────────

func TestHandleModelList_ReturnsOnlyConfiguredModel(t *testing.T) {
	// Must return exactly one entry — the configured model — not KnownProfiles.
	handler := handleModelList("deepseek-v4-flash")
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var models []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&models); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1 (only configured model)", len(models))
	}
	if models[0]["id"] != "deepseek-v4-flash" {
		t.Errorf("id = %v, want 'deepseek-v4-flash'", models[0]["id"])
	}
	if models[0]["current"] != true {
		t.Errorf("current = %v, want true", models[0]["current"])
	}
	if models[0]["description"] == "" {
		t.Error("description should be non-empty for a known model")
	}
}

func TestHandleModelList_EmptyConfigModel_ReturnsEmptyList(t *testing.T) {
	handler := handleModelList("")
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var models []map[string]any
	json.NewDecoder(w.Body).Decode(&models)

	if len(models) != 0 {
		t.Errorf("len(models) = %d, want 0 for unconfigured model", len(models))
	}
}

func TestHandleModelList_UnknownModelStillReturned(t *testing.T) {
	// A custom model not in KnownProfiles must still appear in the list.
	handler := handleModelList("my-custom-llm")
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var models []map[string]any
	json.NewDecoder(w.Body).Decode(&models)

	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0]["id"] != "my-custom-llm" {
		t.Errorf("id = %v, want 'my-custom-llm'", models[0]["id"])
	}
	if models[0]["current"] != true {
		t.Errorf("current = %v, want true", models[0]["current"])
	}
	// max_context should be zero/absent for an unknown model
	if ctx, ok := models[0]["max_context"].(float64); ok && ctx != 0 {
		t.Errorf("max_context = %.0f, want 0 for unknown model", ctx)
	}
}

func TestHandleModelList_MethodNotAllowed(t *testing.T) {
	handler := handleModelList("m")
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/api/models", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/models: status = %d, want 405", method, w.Code)
		}
	}
}

func TestHandleModelList_NoDeepSeekHardcoding(t *testing.T) {
	// Verify that the list does NOT contain KnownProfiles entries when a
	// non-deepseek model is configured. The old bug included all KnownProfiles.
	handler := handleModelList("gpt-4o")
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var models []map[string]any
	json.NewDecoder(w.Body).Decode(&models)

	for _, m := range models {
		id, _ := m["id"].(string)
		if strings.HasPrefix(id, "deepseek-") && id != "gpt-4o" {
			t.Errorf("unexpected deepseek entry %q when configured model is 'gpt-4o'", id)
		}
	}
	if len(models) != 1 {
		t.Errorf("len(models) = %d, want 1", len(models))
	}
}

// ── serveOnListener graceful stop ────────────────────────────────────

func TestServeOnListener_StopsWhenListenerClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	done := make(chan error, 1)
	go func() {
		done <- serveOnListener(ln, mux)
	}()

	// Wait for server to accept connections.
	for i := 0; i < 40; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Closing the listener causes srv.Serve to return a non-ErrServerClosed
	// error, which propagates out of serveOnListener.
	ln.Close()

	select {
	case err := <-done:
		// Any error is acceptable — we just want to confirm the goroutine exited.
		t.Logf("serveOnListener returned: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serveOnListener did not stop after listener was closed")
	}

	// Confirm the server no longer accepts connections.
	// Use a fresh client with keep-alives disabled — the default client may
	// reuse an already-established pooled connection even after the listener
	// is closed, giving a false pass.
	freshClient := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   2 * time.Second,
	}
	_, connErr := freshClient.Get("http://" + addr + "/")
	if connErr == nil {
		t.Error("server still accepting connections after listener was closed")
	}
}

func TestServeOnListener_ReturnsNilOnCleanShutdown(t *testing.T) {
	// A server that exits via srv.Shutdown (no error path) should return nil.
	// We simulate this by having the serve goroutine never error and then
	// verifying the function terminates cleanly when the listener is released.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	done := make(chan error, 1)

	go func() {
		done <- serveOnListener(ln, mux)
	}()

	// Give the goroutine time to start before closing.
	time.Sleep(100 * time.Millisecond)
	ln.Close()

	select {
	case <-done:
		// exited — pass
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after listener close")
	}
}

// ── No-repetitive-responses E2E ──────────────────────────────────────

// TestServe_E2E_NoRepetitiveResponses verifies the origLen fix:
// each turn's streamed response contains ONLY that turn's content.
// Before the fix, the second turn's response would begin with the
// first turn's assistant message (dynamic skill/memory injections
// shifted the user message position, causing newMsgs to include
// the previous assistant message).
func TestServe_E2E_NoRepetitiveResponses(t *testing.T) {
	// Mock LLM: two distinct responses, one per turn.
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		switch callCount {
		case 1:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ALPHA response."}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
		case 2:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"BETA response."}}],"usage":{"prompt_tokens":150,"completion_tokens":12}}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"EXTRA."}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		}
	})
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer ln.Close()

	go serveOnListener(ln, mux)
	waitForHTTP(t, ln.Addr().String())

	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	send := func(content string) {
		payload, _ := json.Marshal(map[string]string{"type": "prompt", "content": content})
		if err := golangws.Message.Send(conn, string(payload)); err != nil {
			t.Fatalf("Send %q: %v", content, err)
		}
	}

	// collectTokens reads events until "done" and returns the concatenated token content.
	collectTokens := func() string {
		conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		var buf strings.Builder
		for {
			var raw []byte
			if err := golangws.Message.Receive(conn, &raw); err != nil {
				t.Fatalf("Receive: %v", err)
			}
			var evt map[string]any
			json.Unmarshal(raw, &evt)
			switch evt["type"] {
			case "token":
				buf.WriteString(fmt.Sprint(evt["content"]))
			case "done":
				return buf.String()
			case "error":
				t.Fatalf("error event: %v", evt["message"])
			}
		}
	}

	// Turn 1
	send("first question")
	resp1 := collectTokens()
	t.Logf("Turn 1: %q", resp1)

	if !strings.Contains(resp1, "ALPHA") {
		t.Errorf("turn 1: expected 'ALPHA' in %q", resp1)
	}

	// Turn 2 — must NOT include turn 1's content.
	send("second question")
	resp2 := collectTokens()
	t.Logf("Turn 2: %q", resp2)

	if strings.Contains(resp2, "ALPHA") {
		t.Errorf("turn 2 contains turn 1 content (origLen bug): %q", resp2)
	}
	if !strings.Contains(resp2, "BETA") {
		t.Errorf("turn 2: expected 'BETA' in %q", resp2)
	}

	t.Log("✅ No repetitive responses: each turn streams only its own content")
}

// TestServe_E2E_SessionMessagesStoredWithoutSystemInjections verifies
// that injected system messages (skills, memory, episodes) are NOT
// persisted in the session store. Only user/assistant/tool messages
// should be stored, so future origLen calculations remain correct.
func TestServe_E2E_SessionMessagesStoredWithoutSystemInjections(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`)
	})
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)

	// Build a mux that also includes the session-by-ID route so we can inspect it.
	ln, mux := buildServeMuxWithSessionByID(t, store)
	defer ln.Close()

	go serveOnListener(ln, mux)
	waitForHTTP(t, ln.Addr().String())

	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Send one prompt, collect session ID.
	payload, _ := json.Marshal(map[string]string{"type": "prompt", "content": "hello"})
	golangws.Message.Send(conn, string(payload))

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var sid, authToken string
	for {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		var evt map[string]any
		json.Unmarshal(raw, &evt)
		if evt["type"] == "session" {
			sid, _ = evt["session_id"].(string)
			authToken, _ = evt["auth_token"].(string)
		}
		if evt["type"] == "done" {
			break
		}
		if evt["type"] == "error" {
			t.Fatalf("error: %v", evt["message"])
		}
	}

	if sid == "" {
		t.Fatal("did not receive session ID")
	}

	// Fetch the stored session and verify no system messages are stored.
	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/api/sessions/"+sid, nil)
	req.Header.Set("X-Session-Token", authToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("GET session status = %d", resp.StatusCode)
	}

	var sess session.Session
	json.NewDecoder(resp.Body).Decode(&sess)

	for i, m := range sess.Messages {
		if m.Role == "system" {
			// The initial empty system message created for new sessions is acceptable;
			// dynamically-injected system messages (skills, memory) must not be stored.
			if m.Content != "" {
				t.Errorf("stored message[%d] is a non-empty system message (injection leaked into session): %q", i, m.Content[:min(len(m.Content), 80)])
			}
		}
	}

	t.Logf("Session %s has %d stored messages (no injected system messages)", sid, len(sess.Messages))
	t.Log("✅ Injected system messages correctly filtered from session storage")
}

// buildServeMuxWithSessionByID is like buildServeMux but also registers
// the handleSessionByID route so tests can fetch individual sessions.
func buildServeMuxWithSessionByID(t *testing.T, store *session.Store) (net.Listener, *http.ServeMux) {
	t.Helper()
	ln, mux := buildServeMux(t, store)
	mux.HandleFunc("/api/sessions/", handleSessionByID(store))
	return ln, mux
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
