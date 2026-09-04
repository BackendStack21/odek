package main

import (
	"encoding/json"
	"github.com/BackendStack21/odek/internal/session"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// readUntilDone receives WS frames until a "done" event arrives (or the
// deadline / error budget is exhausted). E2E tests use it to synchronize
// with prompt completion.
func readUntilDone(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for i := 0; i < 200; i++ {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v", i, err)
		}
		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			continue
		}
		if evt["type"] == "done" {
			return
		}
		if evt["type"] == "error" {
			t.Fatalf("unexpected error event: %s", string(raw))
		}
	}
	t.Fatal("no done event received")
}

// TestServe_E2E_PromptPathSessionSwitch_ClearsStaleBuffer pins buffer
// isolation on the PROMPT-path session switch (a prompt carrying a
// different session_id, without a session_switch frame).
//
// The session_switch handler resets the in-memory buffer
// (ClearBuffer + conditional RestoreBuffer), but the prompt path only
// restored — it never cleared. Switching to a session whose saved buffer
// is EMPTY kept the previous session's lines live: the turn ran with the
// old session's context and the post-turn save persisted those lines into
// the new session's file (cross-session buffer bleed).
func TestServe_E2E_PromptPathSessionSwitch_ClearsStaleBuffer(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	})
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)

	// Session A carries a distinctive buffer line.
	sessA, err := store.Create(
		[]session.Message{{Role: "system", Content: ""}, {Role: "user", Content: "A prompt"}},
		"test-model", "A",
	)
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	sessA.Buffer = []string{"stale-buffer-line-from-A"}
	if err := store.Save(sessA); err != nil {
		t.Fatalf("Save A: %v", err)
	}

	// Session B has an empty saved buffer.
	sessB, err := store.Create(
		[]session.Message{{Role: "system", Content: ""}, {Role: "user", Content: "B prompt"}},
		"test-model", "B",
	)
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	ln, mux := buildServeMux(t, store)
	defer ln.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	sendPrompt := func(sessionID, authToken, content string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{
			"type":       "prompt",
			"content":    content,
			"session_id": sessionID,
			"auth_token": authToken,
		})
		if err := websocket.Message.Send(conn, string(payload)); err != nil {
			t.Fatalf("Send: %v", err)
		}
		readUntilDone(t, conn)
	}

	// 1. Prompt on A — restores A's buffer into the live memory manager.
	sendPrompt(sessA.ID, sessA.AuthToken, "hi")
	// 2. Prompt switching to B via the prompt path (no session_switch).
	sendPrompt(sessB.ID, sessB.AuthToken, "hi again")

	reloaded, err := store.Load(sessB.ID)
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	for _, line := range reloaded.Buffer {
		if strings.Contains(line, "stale-buffer-line-from-A") {
			t.Fatalf("session B's buffer contains session A's line (cross-session buffer bleed): %q", reloaded.Buffer)
		}
	}
}
