package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

type initialTool struct {
	name  string
	calls int
	mu    sync.Mutex
}

func (t *initialTool) Name() string        { return t.name }
func (t *initialTool) Description() string { return "initial test tool" }
func (t *initialTool) Schema() any         { return map[string]any{"type": "object"} }
func (t *initialTool) Call(string) (string, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return "initial-result", nil
}
func (t *initialTool) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }

func TestEngineInitialToolCallsRunBeforeFirstRequestAndAreOneShot(t *testing.T) {
	var requests []map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	initial := &initialTool{name: "initial"}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{initial}), 4, "", nil, 0)
	e.SetInitialToolCalls([]session.ToolCall{acceptanceCall("initial-1", "initial", `{}`)})
	if _, err := e.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if initial.count() != 1 {
		t.Fatalf("initial calls=%d want 1", initial.count())
	}
	mu.Lock()
	first := requests[0]
	mu.Unlock()
	msgs, _ := first["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("first request messages=%d, want initial call transcript", len(msgs))
	}
	found := false
	for _, raw := range msgs {
		if m, ok := raw.(map[string]any); ok && strings.Contains(m["content"].(string), "initial-result") {
			found = true
		}
	}
	if !found {
		t.Fatal("initial tool result missing from first provider request")
	}
	if _, err := e.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if initial.count() != 1 {
		t.Fatalf("initial calls after second run=%d, want one-shot", initial.count())
	}
}

func TestEngineInitialToolCallsCopyInputAndPersistEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	defer srv.Close()
	initial := &initialTool{name: "initial"}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{initial}), 4, "", nil, 0)
	var persisted [][]session.Message
	var events []string
	e.SetMessagesPersistCallback(func(m []session.Message) { persisted = append(persisted, m) })
	e.SetToolEventHandler(func(event, name, data string) { events = append(events, event+":"+name) })
	calls := []session.ToolCall{acceptanceCall("initial-1", "initial", `{}`)}
	e.SetInitialToolCalls(calls)
	calls[0].Function.Arguments = `{"tampered":true}`
	if _, err := e.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if len(persisted) == 0 {
		t.Fatal("initial execution was not persisted")
	}
	if len(events) < 2 || events[0] != "tool_call:initial" || events[1] != "tool_result:initial" {
		t.Fatalf("events=%v", events)
	}
}

func TestEngineInitialToolCallsHonorCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"}}]}`))
	}))
	defer srv.Close()
	initial := &initialTool{name: "initial"}
	e := New(testChatClient(t, srv.URL), tool.NewRegistry([]tool.Tool{initial}), 4, "", nil, 0)
	e.SetInitialToolCalls([]session.ToolCall{acceptanceCall("initial-1", "initial", `{}`)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx, "cancel"); err == nil {
		t.Fatal("cancelled initial run returned nil")
	}
	if initial.count() != 0 {
		t.Fatalf("initial calls=%d after cancellation, want 0", initial.count())
	}
}
