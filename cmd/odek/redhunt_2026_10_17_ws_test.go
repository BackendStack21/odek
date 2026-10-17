package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
