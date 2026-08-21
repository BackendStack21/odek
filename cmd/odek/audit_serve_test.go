package main

// Regression tests for the 2026-08 security audit — serve/API quick wins.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/session"

	golangws "golang.org/x/net/websocket"
)

// TestAudit_ExportMarkdown_FenceBreakout audits the finding that the
// markdown export used a fixed 4-backtick fence: any transcript line of
// exactly ```` closed it early, letting model/tool output forge document
// structure in the "human-shareable" export. The fence must grow longer
// than the longest backtick run in the fenced content.
func TestAudit_ExportMarkdown_FenceBreakout(t *testing.T) {
	sess := &session.Session{
		ID:   "audit-fence-test",
		Task: "fence test",
		Messages: []llm.Message{
			{Role: "user", Content: "check this"},
			{Role: "assistant", Content: "````\n# FORGED HEADING\n```normal```\n````"},
			{Role: "tool", Name: "browser", Content: "````\n## forged tool section\n````"},
		},
	}
	out := exportSessionMarkdown(sess)
	// The assistant content's longest run is 4 backticks → fence must be ≥5.
	// A 5-backtick opener cannot be closed by any 4-backtick line in the
	// body, so the forged structure stays inside the code block.
	if !strings.Contains(out, "`````markdown") {
		t.Errorf("expected a 5-backtick fence around assistant content:\n%s", out)
	}
	if !strings.Contains(out, "`````text") {
		t.Errorf("expected a 5-backtick fence around tool content:\n%s", out)
	}
}

// TestAudit_CodeFence pins the fence-length computation directly.
func TestAudit_CodeFence(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{"plain text", 4},
		{"has `one` backtick", 4},
		{"has ```three```", 4},
		{"has ````four````", 5},
		{"has ```````seven```````", 8},
	}
	for _, c := range cases {
		if got := len(codeFence(c.body)); got != c.want {
			t.Errorf("codeFence(%q) length = %d, want %d", c.body, got, c.want)
		}
	}
}

// TestAudit_ServeHTTPServerTimeouts pins the slowloris hardening: without
// ReadHeaderTimeout/IdleTimeout, any unauthenticated client could hold
// half-open connections forever (rate limiting and the CSRF token only
// apply after the request line arrives).
func TestAudit_ServeHTTPServerTimeouts(t *testing.T) {
	srv := newServeHTTPServer(http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want > 0", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want > 0", srv.IdleTimeout)
	}
}

// TestAudit_IDGenerators pins the conn/run ID generators: correct prefix,
// fixed length, and uniqueness. (The 2026-08 fix made them fail closed on
// crypto/rand errors; the happy path must keep producing distinct IDs.)
func TestAudit_IDGenerators(t *testing.T) {
	seenConn := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newWSConnID()
		if !strings.HasPrefix(id, "conn-") || len(id) != len("conn-")+16 {
			t.Fatalf("newWSConnID() = %q, want conn- + 16 hex chars", id)
		}
		if seenConn[id] {
			t.Fatalf("newWSConnID() repeated: %s", id)
		}
		seenConn[id] = true
	}
	seenRun := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newRunID()
		if !strings.HasPrefix(id, "run-") || len(id) != len("run-")+20 {
			t.Fatalf("newRunID() = %q, want run- + 20 hex chars", id)
		}
		if seenRun[id] {
			t.Fatalf("newRunID() repeated: %s", id)
		}
		seenRun[id] = true
	}
}

// TestAudit_WebSocketInvalidModelRejectedEarly pins the WS-loop ordering
// fix: an invalid model ID must be rejected before the switch is applied to
// the agent (a rejected ID used to stay active and silently reused by the
// next prompt sent without a model field). Runs the production handleWS
// loop with the mock-LLM harness so agent construction succeeds without a
// real API key; the invalid model is rejected before any LLM call, so the
// mock never answers.
func TestAudit_WebSocketInvalidModelRejectedEarly(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer llmSrv.Close()
	cleanupEnv := setTestEnv(t, llmSrv.URL)
	defer cleanupEnv()

	// Build the store directly under setTestEnv's HOME rather than via
	// newTestSessionStore: that helper captures the *current* HOME (already
	// switched) in its t.Cleanup, so its restore would re-leak this test's
	// temp HOME over setTestEnv's restore and poison every later test's
	// os.UserHomeDir(). setTestEnv's cleanup already scrubs this HOME's
	// .odek/sessions tree.
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	ln, mux := buildServeMux(t, store)
	defer ln.Close()
	go func() { _ = serveOnListener(ln, mux) }()
	waitForHTTP(t, ln.Addr().String())

	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()

	msg := map[string]string{"type": "prompt", "content": "hi", "model": "BAD MODEL!"}
	payload, _ := json.Marshal(msg)
	if err := golangws.Message.Send(conn, string(payload)); err != nil {
		t.Fatalf("Send(): %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 5; i++ {
		var data []byte
		if err := golangws.Message.Receive(conn, &data); err != nil {
			t.Fatalf("Receive event %d: %v", i, err)
		}
		var evt map[string]any
		if err := json.Unmarshal(data, &evt); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		if evt["type"] == "error" {
			if got, _ := evt["message"].(string); !strings.Contains(got, "invalid model ID") {
				t.Fatalf("error message = %q, want 'invalid model ID'", got)
			}
			return
		}
	}
	t.Fatal("no invalid-model error event received")
}

// TestAudit_ResumeTaskPreview pins the Telegram /resume guard: a zero-message
// session must render an empty preview (the handler then reports "(empty)")
// instead of panicking on messages[0].
func TestAudit_ResumeTaskPreview(t *testing.T) {
	if got := resumeTaskPreview(nil); got != "" {
		t.Errorf("resumeTaskPreview(nil) = %q, want \"\"", got)
	}
	if got := resumeTaskPreview([]llm.Message{}); got != "" {
		t.Errorf("resumeTaskPreview(empty) = %q, want \"\"", got)
	}
	if got := resumeTaskPreview([]llm.Message{{Role: "user", Content: "short task"}}); got != "short task" {
		t.Errorf("resumeTaskPreview(short) = %q, want %q", got, "short task")
	}
	long := strings.Repeat("x", 200)
	got := resumeTaskPreview([]llm.Message{{Role: "user", Content: long}})
	if runes := len([]rune(got)); runes != 81 || !strings.HasSuffix(got, "…") {
		t.Errorf("resumeTaskPreview(long) = %d runes, want 81 with ellipsis suffix", runes)
	}
}
