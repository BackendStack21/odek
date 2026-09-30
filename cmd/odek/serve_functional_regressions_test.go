package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/resource"
	"golang.org/x/net/websocket"
)

func TestRegression_AttachmentOnlyPromptStartsTurn(t *testing.T) {
	llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	defer llm.Close()
	defer setTestEnv(t, llm.URL)()
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := store.Create(nil, "test-model", "barrier")
	if err != nil {
		t.Fatal(err)
	}
	ln, mux := buildServeMux(t, store)
	defer startServeTest(t, ln, mux)()
	wsUpgradeLimiter.reset()
	conn := dialTestWS(t, ln.Addr().String())
	defer conn.Close()
	// A following session_switch acts as a FIFO barrier: receiving its
	// session event proves the preceding attachment prompt was processed.
	for _, payload := range []any{
		map[string]any{"type": "prompt", "content": "", "session_id": sess.ID, "auth_token": sess.AuthToken, "attachments": []map[string]string{{"name": "notes.txt", "content": "Summarize these notes"}}},
		map[string]any{"type": "session_switch", "session_id": barrier.ID, "auth_token": barrier.AuthToken},
	} {
		data, _ := json.Marshal(payload)
		if err := websocket.Message.Send(conn, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	started := false
	for range 100 {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		if event["type"] == "turn_started" {
			started = true
		}
		if event["type"] == "session" && event["session_id"] == barrier.ID {
			break
		}
	}
	if !started {
		t.Fatal("attachment-only prompt was silently discarded; no turn_started, done, or error was returned")
	}
}

func TestRegression_UploadUsesImplicitCurrentSession(t *testing.T) {
	var calls atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer llm.Close()
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0}
	req := httptest.NewRequest("POST", "/api/uploads?name=image.png&session_id="+sess.ID, bytes.NewReader(png))
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	workspace := t.TempDir()
	handleBrowserUpload(store, "test-model", workspace)(w, req)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	var upload struct {
		ID string `json:"upload_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	a, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	resolved := loadJSONMockResolved()
	var frames []map[string]any
	msg := wsClientMsg{Type: "prompt", Content: "inspect this image", Attachments: []wsAttachment{{UploadID: upload.ID}}}
	handlePrompt(context.Background(), func(f map[string]any) { frames = append(frames, f) }, store, resource.NewRegistry(), resolved, a, nil, sess, msg, new(int), new(int), nil, nil, nil, nil)
	for _, f := range frames {
		if f["type"] == "error" {
			t.Fatalf("current session's upload was rejected: %v", f["message"])
		}
	}
	if calls.Load() == 0 {
		t.Fatal("valid attachment did not reach provider")
	}
}

func TestRegression_FailedTurnStillRecordsPaidUsage(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"partial answer"},"finish_reason":"length"}],"usage":{"prompt_tokens":123,"completion_tokens":45}}`)
	}))
	defer llm.Close()
	store := newTestSessionStore(t)
	sess, err := store.Create(nil, "test-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	a, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Help", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	beforeIn, beforeOut := atomic.LoadInt64(&serveStats.TokensIn), atomic.LoadInt64(&serveStats.TokensOut)
	var failure string
	handlePrompt(context.Background(), func(f map[string]any) {
		if f["type"] == "error" {
			failure, _ = f["message"].(string)
		}
	}, store, resource.NewRegistry(), loadJSONMockResolved(), a, nil, sess, wsClientMsg{Type: "prompt", Content: "hello"}, new(int), new(int), nil, nil, nil, nil)
	if !strings.Contains(failure, "incomplete") || a.TotalInputTokens() != 123 || a.TotalOutputTokens() != 45 {
		t.Fatalf("fixture did not produce paid partial response: err=%q usage=%d/%d", failure, a.TotalInputTokens(), a.TotalOutputTokens())
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.InputTokens != 123 || saved.OutputTokens != 45 {
		t.Errorf("paid failed turn omitted from session usage: got %d/%d want 123/45", saved.InputTokens, saved.OutputTokens)
	}
	if in, out := atomic.LoadInt64(&serveStats.TokensIn)-beforeIn, atomic.LoadInt64(&serveStats.TokensOut)-beforeOut; in != 123 || out != 45 {
		t.Errorf("paid failed turn omitted from server usage: got %d/%d want 123/45", in, out)
	}
}

func TestRESTAttachmentOnlyPromptCompletes(t *testing.T) {
	llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"notes summarized"}}]}`)
	})
	defer llm.Close()
	env := newRestRunEnv(t, llm.URL, nil)
	_, response := startTestRun(t, env, `{"attachments":[{"name":"notes.txt","content":"summarize these notes"}]}`)
	snapshot := waitRunStatus(t, response["run_id"].(string), 5*time.Second)
	if snapshot["status"] != "completed" || snapshot["result"] != "notes summarized" {
		t.Fatalf("attachment-only REST run failed: %v", snapshot)
	}
}

func TestEmptyAttachmentsRejectedBeforeAgentCreation(t *testing.T) {
	for _, attachments := range [][]wsAttachment{
		nil, {{}}, {{Name: "empty.txt"}}, {{Content: "no filename"}},
	} {
		_, err := startServeRun(loadJSONMockResolved(), "Help", nil, nil, promptRequest{Content: "  ", Attachments: attachments})
		if err == nil || !strings.Contains(err.Error(), "content required") {
			t.Fatalf("empty attachment placeholders admitted: attachments=%v err=%v", attachments, err)
		}
	}
}

func TestRESTFailedTurnKeepsRunUsage(t *testing.T) {
	llm := mockLLM(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"partial answer"},"finish_reason":"length"}],"usage":{"prompt_tokens":123,"completion_tokens":45}}`)
	})
	defer llm.Close()
	env := newRestRunEnv(t, llm.URL, nil)
	_, response := startTestRun(t, env, `{"content":"hello"}`)
	snapshot := waitRunStatus(t, response["run_id"].(string), 5*time.Second)
	if snapshot["status"] != "failed" || snapshot["input_tokens"] != int64(123) || snapshot["output_tokens"] != int64(45) {
		t.Fatalf("failed REST run lost paid usage: %v", snapshot)
	}
}
