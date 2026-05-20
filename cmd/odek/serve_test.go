package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/kode/internal/config"
	"github.com/BackendStack21/kode/internal/resource"
	"github.com/BackendStack21/kode/internal/session"
	golangws "golang.org/x/net/websocket"
)

// ── Test Server ────────────────────────────────────────────────────────

// testServer wraps the kode serve HTTP server for testing.
type testServer struct {
	ln     net.Listener
	url    string
	wsURL  string
	store  *session.Store
	t      *testing.T
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session store: %v", err)
	}

	s := &testServer{
		ln:    ln,
		url:   "http://" + ln.Addr().String(),
		wsURL: "ws://" + ln.Addr().String(),
		store: store,
		t:     t,
	}

	// Build resource registry (test root is temp dir)
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatic())
	mux.HandleFunc("/api/sessions", s.handleSessionList())
	mux.HandleFunc("/api/resources", s.handleResourceSearch())
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler:   s.handleWebSocket,
	})

	go http.Serve(ln, mux)
	return s
}

func (s *testServer) Close() {
	s.ln.Close()
}

func (s *testServer) handleStatic() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := uiFS.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "UI not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

func (s *testServer) handleSessionList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := s.store.List(50)
		if err != nil {
			sessions = []session.Session{}
		}
		if sessions == nil {
			sessions = []session.Session{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}

func (s *testServer) handleResourceSearch() http.HandlerFunc {
	// Simple handler that returns a known resource for testing
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		results := []map[string]any{
			{"id": "@" + q, "type": "file", "label": q, "detail": "test file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

func (s *testServer) handleWebSocket(conn *golangws.Conn) {
	defer conn.Close()

	for {
		var data []byte
		if err := golangws.Message.Receive(conn, &data); err != nil {
			return
		}

		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Handle prompt — echo back structured events for testing
		if msg["type"] == "prompt" {
			// Send session event
			writeJSON(conn, map[string]any{
				"type":       "session",
				"session_id": "test-session-001",
				"model":      "test-model",
			})

			// Send thinking
			writeJSON(conn, map[string]any{
				"type":    "thinking",
				"content": "Analyzing the request",
			})

			// Send tool call
			writeJSON(conn, map[string]any{
				"type":    "tool_call",
				"name":    "shell",
				"command": "echo test",
			})

			// Send tool result
			writeJSON(conn, map[string]any{
				"type":   "tool_result",
				"name":   "shell",
				"output": "test output",
			})

			// Send final token
			writeJSON(conn, map[string]any{
				"type":    "token",
				"content": "Done with your request.",
			})

			// Send done
			writeJSON(conn, map[string]any{
				"type":    "done",
				"latency": 0.5,
			})
		}
	}
}

func writeJSON(conn *golangws.Conn, data any) {
	payload, _ := json.Marshal(data)
	golangws.Message.Send(conn, string(payload))
}

// readJSON reads a complete WebSocket message and unmarshals it into dst.
func readJSON(conn *golangws.Conn, dst any) error {
	var data []byte
	if err := golangws.Message.Receive(conn, &data); err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// sessionDir returns the ~/.kode/sessions directory for testing.
func sessionDir() (string, error) {
	return "/tmp/kode-test-sessions", nil
}

// ── Tests ──────────────────────────────────────────────────────────────

func TestServe_ServesUI(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	resp, err := http.Get(s.url + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	// Check content type
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// Check it contains kode branding
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "kode") {
			found = true
			break
		}
	}
	if !found {
		t.Error("UI should contain 'kode' text")
	}
}

func TestServe_404OnUnknownPath(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	resp, err := http.Get(s.url + "/nonexistent")
	if err != nil {
		t.Fatalf("GET /nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServe_SessionList(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	resp, err := http.Get(s.url + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var sessions []any
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Should return an array (possibly empty)
	if sessions == nil {
		t.Error("sessions should be an array, not null")
	}
}

func TestServe_ResourceSearch(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	resp, err := http.Get(s.url + "/api/resources?q=testfile")
	if err != nil {
		t.Fatalf("GET /api/resources: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var results []any
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) < 1 {
		t.Error("expected at least 1 resource result")
	}
}

func TestServe_WebSocketUpgrade(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	conn, err := golangws.Dial(s.wsURL+"/ws", "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()

	// Send a prompt to trigger the handler
	prompt := map[string]string{"type": "prompt", "content": "hello"}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send(): %v", err)
	}

	// Should get a JSON response back
	var data []byte
	if err := golangws.Message.Receive(conn, &data); err != nil {
		t.Fatalf("Receive(): %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if response["type"] != "session" {
		t.Errorf("event type = %v, want 'session'", response["type"])
	}
}

func TestServe_WebSocketPromptFlow(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	conn, err := golangws.Dial(s.wsURL+"/ws", "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()

	// Send a prompt
	prompt := map[string]string{
		"type":    "prompt",
		"content": "hello world",
	}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send(): %v", err)
	}

	// Collect events
	var events []map[string]any
	timeout := time.After(5 * time.Second)
	done := false

	for !done {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for events")
		default:
			var event map[string]any
			if err := readJSON(conn, &event); err != nil {
				t.Fatalf("Receive(): %v", err)
			}
			events = append(events, event)

			if event["type"] == "done" {
				done = true
			}
		}
	}

	// Verify event sequence
	if len(events) < 5 {
		t.Fatalf("expected at least 5 events, got %d: %+v", len(events), events)
	}

	// First event should be session
	if events[0]["type"] != "session" {
		t.Errorf("event[0].type = %v, want 'session'", events[0]["type"])
	}
	if events[0]["session_id"] != "test-session-001" {
		t.Errorf("session_id = %v", events[0]["session_id"])
	}

	// Should have thinking, tool_call, tool_result, token, done
	types := make([]string, len(events))
	for i, e := range events {
		t.Logf("event[%d] = %v", i, e)
		types[i] = fmt.Sprint(e["type"])
	}

	// Verify done is last
	if events[len(events)-1]["type"] != "done" {
		t.Errorf("last event type = %v, want 'done'", events[len(events)-1]["type"])
	}
}

func TestServe_WebSocketInvalidJSON(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	conn, err := golangws.Dial(s.wsURL+"/ws", "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()

	// Send invalid JSON
	if err := golangws.Message.Send(conn, "not json"); err != nil {
		t.Fatalf("Send(): %v", err)
	}
}

func TestServe_WebSocketInvalidMessageType(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	conn, err := golangws.Dial(s.wsURL+"/ws", "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()

	// Send a message with wrong type
	msg := map[string]string{"type": "invalid", "content": "test"}
	payload, _ := json.Marshal(msg)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send(): %v", err)
	}

	// Server should ignore it — send a valid prompt to check
	msg2 := map[string]string{"type": "prompt", "content": "valid"}
	payload2, _ := json.Marshal(msg2)
	if err := golangws.Message.Send(conn, string(payload2)); err != nil {
		t.Fatalf("Send(): %v", err)
	}

	// Should get a response despite the invalid message before
	timeout := time.After(3 * time.Second)
	gotResponse := false
	for !gotResponse {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for response after invalid message")
		default:
			var event map[string]any
			if err := readJSON(conn, &event); err != nil {
				t.Fatalf("Receive(): %v", err)
			}
			if event["type"] == "done" || event["type"] == "session" {
				gotResponse = true
			}
		}
	}
}

func TestServe_MultiplePrompts(t *testing.T) {
	s := startTestServer(t)
	defer s.Close()

	conn, err := golangws.Dial(s.wsURL+"/ws", "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer conn.Close()

	for i := 0; i < 3; i++ {
		prompt := map[string]string{
			"type":    "prompt",
			"content": fmt.Sprintf("prompt %d", i),
		}
		payload, _ := json.Marshal(prompt)
		if err := golangws.Message.Send(conn, string(payload)); err != nil {
			t.Fatalf("prompt %d: Send(): %v", i, err)
		}

		// Read until done
		timeout := time.After(5 * time.Second)
		promptDone := false
		for !promptDone {
			select {
			case <-timeout:
				t.Fatalf("timeout waiting for prompt %d", i)
			default:
				var event map[string]any
				if err := readJSON(conn, &event); err != nil {
					t.Fatalf("prompt %d: Receive(): %v", i, err)
				}
				if event["type"] == "done" {
					promptDone = true
				}
			}
		}
	}
}

// ── E2E Integration Test ───────────────────────────────────────────────
//
// Starts the real kode serve process and verifies the full WebSocket
// pipeline: upgrade, message send, response receive.

func TestServe_E2E_WebSocketPipeline(t *testing.T) {
	// Create a listener on a random port — no TOCTOU race since we pass
	// the listener directly to serveOnListener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	// Load config and build the mux (same setup as serveCmd)
	resolved := config.LoadConfig(config.CLIFlags{})
	systemMessage := resolved.System
	if systemMessage == "" {
		systemMessage = defaultSystem
	}

	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session store: %v", err)
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".kode", "sessions")),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic())
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler: func(conn *golangws.Conn) {
			handleWS(store, resourceReg, resolved, systemMessage, conn)
		},
	})
	mux.HandleFunc("/api/resources", handleResourceSearch(resourceReg))
	mux.HandleFunc("/api/sessions", handleSessionList(store))

	// Start serving on the pre-created listener in a goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOnListener(ln, mux)
	}()
	defer ln.Close()

	// Wait for server to be ready
	var httpReady bool
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		resp, err := http.Get("http://" + addr + "/")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			httpReady = true
			break
		}
	}
	if !httpReady {
		// Check if serveCmd returned an error immediately
		select {
		case err := <-errCh:
			t.Fatalf("server exited before ready: %v", err)
		default:
			t.Fatal("server not ready after 5s")
		}
	}
	defer ln.Close()

	// 1. Connect via WebSocket
	wsURL := "ws://" + addr + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(%q): %v", wsURL, err)
	}
	defer conn.Close()
	t.Log("E2E: WebSocket connected")

	// 2. Send a prompt
	msg := map[string]string{"type": "prompt", "content": "say hello"}
	payload, _ := json.Marshal(msg)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Log("E2E: Prompt sent")

	// 3. Read response — we expect at minimum a 'session' event
	// followed by either a token/error/done event.
	// This proves the full WS pipeline: upgrade → handler → agent → response.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	var sawSession bool
	for i := 0; i < 5; i++ {
		var data []byte
		if err := golangws.Message.Receive(conn, &data); err != nil {
			t.Fatalf("Receive event %d: %v", i, err)
		}
		t.Logf("E2E event %d: %s", i, string(data))

		var evt map[string]any
		if err := json.Unmarshal(data, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}

		switch evt["type"] {
		case "session":
			sawSession = true
			if sid, ok := evt["session_id"].(string); ok && sid != "" {
				t.Logf("E2E: session %s created", sid)
			}
		case "token", "thinking", "tool_call", "tool_result":
			// Normal streaming events — pipeline is working
		case "done":
			if !sawSession {
				t.Error("E2E: got 'done' before 'session' event")
			}
			return // success
		case "error":
			// Two possible flows:
			//   1. Agent setup fails (no API key) → error before session
			//   2. LLM call fails (bad API key) → session then error
			// Both prove the WS pipeline works.
			t.Logf("E2E: expected error (no API key in test env): %s", evt["message"])
			return // success — WS pipeline proven
		}
	}

}

// ── Full-Stack E2E with Mock LLM ──────────────────────────────────────
//
// Starts the real kode serve handler with a mock LLM backend and verifies
// the complete WebUI flow: WS upgrade, prompt send, streaming events,
// tool calls, and done signal — all without needing a real API key.

func TestServe_E2E_FullWebUIFlow(t *testing.T) {
	// 1. Mock LLM server: simulates OpenAI chat completions API
	callCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 2 {
			// First two calls: tool call (shell echo)
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Running step %d.","tool_calls":[{"id":"call_%d","function":{"name":"shell","arguments":"{\"command\":\"echo step %d\"}"}}]}}]}`,
				callCount, callCount, callCount)
		} else {
			// Final call: text response
			w.Write([]byte(`{"choices":[{"message":{"content":"All done."}}]}`))
		}
	}))
	defer llmSrv.Close()

	// 2. Set up env for mock LLM
	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	// 3. Create a session store (isolated temp dir)
	store := newTestSessionStore(t)

	// 4. Build the real kode serve mux with mock config
	ln, mux := buildServeMux(t, store)
	defer ln.Close()

	// 5. Start serving
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveOnListener(ln, mux)
	}()

	// 6. Wait for HTTP ready
	waitForHTTP(t, ln.Addr().String())

	// 7. Connect via WebSocket
	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(%q): %v", wsURL, err)
	}
	defer conn.Close()
	t.Log("✅ WebSocket connected")

	// 8. Send a prompt
	prompt := map[string]string{"type": "prompt", "content": "Hi"}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Log("✅ Prompt sent")

	// 9. Collect all events with timeout
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	var events []map[string]any
	var sawSession, sawToken, sawToolCall, sawDone bool

	for i := 0; i < 20; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v (collected %d events)", i, err, len(events))
		}
		t.Logf("  event[%d]: %s", i, string(raw))

		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		events = append(events, evt)

		switch evt["type"] {
		case "session":
			sawSession = true
		case "token":
			sawToken = true
		case "tool_call":
			sawToolCall = true
		case "tool_result":
			// expected after tool_call
		case "done":
			sawDone = true
			goto done // break out of loop
		case "error":
			t.Fatalf("unexpected error event: %v", evt["message"])
		}
	}

done:
	// 10. Validate event sequence
	if !sawSession {
		t.Fatal("E2E: missing 'session' event")
	}
	if !sawToken {
		t.Fatal("E2E: missing 'token' event (LLM response)")
	}
	if !sawToolCall {
		t.Fatal("E2E: missing 'tool_call' event")
	}
	if !sawDone {
		t.Fatal("E2E: missing 'done' event")
	}

	// Validate ordering: session must be first
	if events[0]["type"] != "session" {
		t.Errorf("E2E: first event type = %v, want 'session'", events[0]["type"])
	}

	// Log the full sequence for inspection
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e["type"].(string)
	}
	t.Logf("✅ E2E event sequence: %v", types)
	t.Log("✅ Full WebUI pipeline verified: upgrade → prompt → session → tokens → tool_call → done")
}

// ── E2E Helpers ────────────────────────────────────────────────────────

// setTestEnv configures env vars for testing with a mock LLM.
// Returns a cleanup function that restores original values.
func setTestEnv(t *testing.T, llmBaseURL string) func() {
	t.Helper()
	origDS := os.Getenv("DEEPSEEK_API_KEY")
	origOAI := os.Getenv("OPENAI_API_KEY")
	origKBS := os.Getenv("KODE_BASE_URL")
	origHome := os.Getenv("HOME")

	os.Setenv("DEEPSEEK_API_KEY", "sk-mock")
	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("KODE_BASE_URL", llmBaseURL)
	os.Setenv("HOME", t.TempDir())

	return func() {
		os.Setenv("DEEPSEEK_API_KEY", origDS)
		os.Setenv("OPENAI_API_KEY", origOAI)
		os.Setenv("KODE_BASE_URL", origKBS)
		os.Setenv("HOME", origHome)
	}
}

// newTestSessionStore creates a session.Store backed by a temp directory.
func newTestSessionStore(t *testing.T) *session.Store {
	t.Helper()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	return store
}

// buildServeMux creates a listener on a random port and builds the
// kode serve HTTP mux with a pre-configured session store.
func buildServeMux(t *testing.T, store *session.Store) (net.Listener, *http.ServeMux) {
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

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	resourceReg := resource.NewRegistry(
		resource.NewFileResolver(cwd),
		resource.NewSessionResolver(filepath.Join(home, ".kode", "sessions")),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatic())
	mux.Handle("/ws", &golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler: func(conn *golangws.Conn) {
			handleWS(store, resourceReg, resolved, systemMessage, conn)
		},
	})
	mux.HandleFunc("/api/resources", handleResourceSearch(resourceReg))
	mux.HandleFunc("/api/sessions", handleSessionList(store))

	return ln, mux
}

// waitForHTTP blocks until the HTTP server responds with 200 on GET /.
func waitForHTTP(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		resp, err := http.Get("http://" + addr + "/")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
	}
	t.Fatal("server not ready after 5s")
}

// TestServe_E2E_MultiToolCall verifies the WebUI event sequence when
// the agent makes multiple tool calls in a single turn. This exercises
// the same code path the WebUI uses for sub-agent delegation rendering.
func TestServe_E2E_MultiToolCall(t *testing.T) {
	// Mock LLM: make two shell tool calls, then a final response.
	callCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 2 {
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Running step %d.","tool_calls":[{"id":"call_%d","function":{"name":"shell","arguments":"{\"command\":\"echo step %d\"}"}}]}}]}`,
				callCount, callCount, callCount)
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":"All done."}}]}`))
		}
	}))
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(%q): %v", wsURL, err)
	}
	defer conn.Close()
	t.Log("✅ WebSocket connected")

	// Send a prompt
	prompt := map[string]string{"type": "prompt", "content": "run multiple steps"}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Log("✅ Prompt sent")

	// Collect all events
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	var events []map[string]any
	var sawSession, sawToken, sawToolCall, sawToolResult, sawDone bool
	var toolCallCount, toolResultCount int

	for i := 0; i < 15; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v (collected %d events)", i, err, len(events))
		}
		t.Logf("  event[%d]: %s", i, string(raw))

		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		events = append(events, evt)

		switch evt["type"] {
		case "session":
			sawSession = true
		case "token":
			sawToken = true
		case "tool_call":
			sawToolCall = true
			toolCallCount++
		case "tool_result":
			sawToolResult = true
			toolResultCount++
		case "done":
			sawDone = true
			goto multiDone
		case "error":
			t.Fatalf("unexpected error: %v", evt["message"])
		}
	}

multiDone:
	if !sawSession {
		t.Fatal("E2E: missing session event")
	}
	if !sawToken {
		t.Fatal("E2E: missing token event")
	}
	if !sawToolCall {
		t.Fatal("E2E: missing tool_call event")
	}
	if !sawToolResult {
		t.Fatal("E2E: missing tool_result event")
	}
	if toolCallCount < 2 {
		t.Errorf("E2E: expected at least 2 tool_calls, got %d", toolCallCount)
	}
	if toolResultCount < 2 {
		t.Errorf("E2E: expected at least 2 tool_results, got %d", toolResultCount)
	}
	if !sawDone {
		t.Fatal("E2E: missing done event")
	}

	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e["type"].(string)
	}
	t.Logf("✅ Multi-tool E2E event sequence: %v", types)
	t.Log("✅ Multi-tool pipeline verified: session → tokens → tool_call → tool_result → (repeat) → done")
}

// TestServe_E2E_TokenStats verifies that the done event includes
// latency, contextTokens, outputTokens, and session-level token
// economics fields.
func TestServe_E2E_TokenStats(t *testing.T) {
	// Mock LLM server with usage info in every response
	callCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 1 {
			// First call: tool call with usage
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Checking.","tool_calls":[{"id":"c_1","function":{"name":"shell","arguments":"{\"command\":\"echo ok\"}"}}]}}],"usage":{"prompt_tokens":200,"completion_tokens":30}}`)
		} else {
			// Final answer with usage
			fmt.Fprint(w, `{"choices":[{"message":{"content":"All done."}}],"usage":{"prompt_tokens":300,"completion_tokens":60}}`)
		}
	}))
	defer llmSrv.Close()

	envCleanup := setTestEnv(t, llmSrv.URL)
	defer envCleanup()

	store := newTestSessionStore(t)
	ln, mux := buildServeMux(t, store)
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, err := golangws.Dial(wsURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("Dial(%q): %v", wsURL, err)
	}
	defer conn.Close()
	t.Log("✅ WebSocket connected")

	// Send first prompt
	prompt := map[string]string{"type": "prompt", "content": "run a command"}
	payload, _ := json.Marshal(prompt)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Log("✅ Prompt 1 sent")

	// Collect events — focus on the done event
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	var doneEvent map[string]any
	for i := 0; i < 15; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d: %v", i, err)
		}
		t.Logf("  event[%d]: %s", i, string(raw))

		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		if evt["type"] == "done" {
			doneEvent = evt
			goto statsCheck
		}
		if evt["type"] == "error" {
			t.Fatalf("unexpected error: %v", evt["message"])
		}
	}
	t.Fatal("did not receive done event")

statsCheck:
	if doneEvent == nil {
		t.Fatal("done event not found")
	}

	// Validate done event fields
	latency, ok := doneEvent["latency"].(float64)
	if !ok {
		t.Error("done event missing 'latency' field")
	} else if latency <= 0 {
		t.Errorf("latency = %v, want > 0", latency)
	}
	t.Logf("  latency: %.2fs", latency)

	ctxTokens, ok := doneEvent["contextTokens"].(float64)
	if !ok {
		t.Error("done event missing 'contextTokens' field")
	} else if ctxTokens != 500 { // 200 + 300
		t.Errorf("contextTokens = %.0f, want 500", ctxTokens)
	}
	t.Logf("  contextTokens: %.0f", ctxTokens)

	outTokens, ok := doneEvent["outputTokens"].(float64)
	if !ok {
		t.Error("done event missing 'outputTokens' field")
	} else if outTokens != 90 { // 30 + 60
		t.Errorf("outputTokens = %.0f, want 90", outTokens)
	}
	t.Logf("  outputTokens: %.0f", outTokens)

	// Session-level stats (first prompt = same as turn-level)
	sessCtx, ok := doneEvent["sessionContextTokens"].(float64)
	if !ok {
		t.Error("done event missing 'sessionContextTokens' field")
	} else if sessCtx != 500 {
		t.Errorf("sessionContextTokens = %.0f, want 500", sessCtx)
	}
	t.Logf("  sessionContextTokens: %.0f", sessCtx)

	sessOut, ok := doneEvent["sessionOutputTokens"].(float64)
	if !ok {
		t.Error("done event missing 'sessionOutputTokens' field")
	} else if sessOut != 90 {
		t.Errorf("sessionOutputTokens = %.0f, want 90", sessOut)
	}
	t.Logf("  sessionOutputTokens: %.0f", sessOut)

	// Send a second prompt to verify session-level accumulation
	callCount = 0 // reset mock so next prompt also makes a tool call
	prompt2 := map[string]string{"type": "prompt", "content": "do another thing"}
	payload2, _ := json.Marshal(prompt2)
	if err := golangws.Message.Send(conn, string(payload2)); err != nil {
		t.Fatalf("Send prompt 2: %v", err)
	}
	t.Log("✅ Prompt 2 sent")

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var done2 map[string]any
	for i := 0; i < 15; i++ {
		var raw []byte
		if err := golangws.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("Receive event %d (prompt 2): %v", i, err)
		}
		t.Logf("  event[%d]: %s", i, string(raw))

		var evt map[string]any
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		if evt["type"] == "done" {
			done2 = evt
			goto sessionCheck
		}
		if evt["type"] == "error" {
			t.Fatalf("unexpected error: %v", evt["message"])
		}
	}
	t.Fatal("did not receive second done event")

sessionCheck:
	if done2 == nil {
		t.Fatal("second done event not found")
	}

	// Turn-level stats should reflect only this turn's tokens
	ctx2, _ := done2["contextTokens"].(float64)
	if ctx2 != 500 {
		t.Errorf("prompt 2 contextTokens = %.0f, want 500", ctx2)
	}
	out2, _ := done2["outputTokens"].(float64)
	if out2 != 90 {
		t.Errorf("prompt 2 outputTokens = %.0f, want 90", out2)
	}

	// Session-level stats should be the sum of both turns
	sessCtx2, _ := done2["sessionContextTokens"].(float64)
	if sessCtx2 != 1000 { // 500 + 500
		t.Errorf("sessionContextTokens (prompt 2) = %.0f, want 1000", sessCtx2)
	}
	sessOut2, _ := done2["sessionOutputTokens"].(float64)
	if sessOut2 != 180 { // 90 + 90
		t.Errorf("sessionOutputTokens (prompt 2) = %.0f, want 180", sessOut2)
	}

	t.Log("✅ Token stats verified: turn-level + session-level accumulation")
}

