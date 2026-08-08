package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// eventCollector collects emitted events in order, thread-safe.
type eventCollector struct {
	mu  sync.Mutex
	evs []events.Event
}

func (c *eventCollector) handle(ev events.Event) {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
}

func (c *eventCollector) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.evs...)
}

func (c *eventCollector) types() []string {
	var out []string
	for _, ev := range c.all() {
		out = append(out, ev.Type)
	}
	return out
}

// newToolLoopServer returns a fake LLM server that requests one tool call on
// the first request and answers with a final message on the second.
func newToolLoopServer(toolName, args string) *httptest.Server {
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprintf(w, `{
				"choices":[{
					"message":{
						"content":"Calling a tool.",
						"tool_calls":[{
							"id":"call_1",
							"function":{"name":%q,"arguments":%q}
						}]
					}
				}],
				"usage":{"prompt_tokens":11,"completion_tokens":7}
			}`, toolName, args)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":23,"completion_tokens":5}}`)
		}
	}))
}

func TestEngine_Events_ToolRunOrderAndShape(t *testing.T) {
	const args = `{"text":"secret-arg-value"}`
	server := newToolLoopServer("echo", args)
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echoes input", output: "hello output"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "Echo hello"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Expected order: tool started → tool completed → iteration 1 completed →
	// iteration 2 (final answer) completed.
	want := []string{
		events.TypeToolCallStarted,
		events.TypeToolCallCompleted,
		events.TypeIterationCompleted,
		events.TypeIterationCompleted,
	}
	got := col.types()
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event types = %v, want %v", got, want)
		}
	}

	evs := col.all()

	// tool_call_started: digest + size only, correct iteration.
	start := evs[0]
	if start.Tool != "echo" || start.Iteration != 1 {
		t.Errorf("start event tool=%q iteration=%d, want echo/1", start.Tool, start.Iteration)
	}
	digest, _ := start.Data["args_sha256"].(string)
	if digest != events.ArgsDigest(args) {
		t.Errorf("args_sha256 = %q, want digest of args", digest)
	}
	if size, _ := start.Data["args_bytes"].(int); size != len(args) {
		t.Errorf("args_bytes = %v, want %d", start.Data["args_bytes"], len(args))
	}

	// tool_call_completed: same tool/iteration, carries duration + result size;
	// correlated with the start event via the shared digest (recomputed here —
	// the completed event deliberately carries no args fields of its own).
	complete := evs[1]
	if complete.Tool != start.Tool || complete.Iteration != start.Iteration {
		t.Errorf("completed event does not correlate with start: %+v vs %+v", complete, start)
	}
	if _, ok := complete.Data["duration_ms"]; !ok {
		t.Error("completed event missing duration_ms")
	}
	if size, _ := complete.Data["result_bytes"].(int); size != len("hello output") {
		t.Errorf("result_bytes = %v, want %d", complete.Data["result_bytes"], len("hello output"))
	}
	if n, _ := complete.Data["artifact_count"].(int); n != 0 {
		t.Errorf("artifact_count = %v, want 0 for plain text result", n)
	}

	// iteration_completed: cumulative tokens + tools_called.
	iter1 := evs[2]
	if iter1.Iteration != 1 {
		t.Errorf("iteration_completed #1 iteration = %d, want 1", iter1.Iteration)
	}
	if n, _ := iter1.Data["tools_called"].(int); n != 1 {
		t.Errorf("iteration 1 tools_called = %v, want 1", n)
	}
	if tok, _ := iter1.Data["input_tokens"].(int); tok != 11 {
		t.Errorf("iteration 1 input_tokens = %v, want 11", tok)
	}
	iter2 := evs[3]
	if n, _ := iter2.Data["tools_called"].(int); n != 0 {
		t.Errorf("final iteration tools_called = %v, want 0", n)
	}
	if tok, _ := iter2.Data["input_tokens"].(int); tok != 34 {
		t.Errorf("final iteration cumulative input_tokens = %v, want 34", tok)
	}
}

func TestEngine_Events_NeverContainRawArgs(t *testing.T) {
	const args = `{"text":"secret-arg-value"}`
	server := newToolLoopServer("echo", args)
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echoes input", output: "hello output"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "Echo hello"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	for _, ev := range col.all() {
		if strings.Contains(fmt.Sprintf("%+v", ev), "secret-arg-value") {
			t.Errorf("event leaks raw tool arguments: %+v", ev)
		}
	}
}

func TestEngine_Events_ToolFailure(t *testing.T) {
	server := newToolLoopServer("boom", `{"x":1}`)
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&failTool{name: "boom"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)

	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	var failed *events.Event
	for i, ev := range col.all() {
		if ev.Type == events.TypeToolCallFailed {
			failed = &col.evs[i]
		}
	}
	if failed == nil {
		t.Fatalf("no tool_call_failed event in %v", col.types())
	}
	if failed.Tool != "boom" {
		t.Errorf("failed event tool = %q, want boom", failed.Tool)
	}
	if failed.Data["error_class"] != "tool_error" {
		t.Errorf("error_class = %v, want tool_error", failed.Data["error_class"])
	}
	if _, ok := failed.Data["duration_ms"]; !ok {
		t.Error("failed event missing duration_ms")
	}
}

// failTool always returns an error.
type failTool struct{ name string }

func (f *failTool) Name() string        { return f.name }
func (f *failTool) Description() string { return "always fails" }
func (f *failTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (f *failTool) Call(args string) (string, error) { return "", fmt.Errorf("kaboom") }

func TestEngine_Events_NilHandlerNoPanic(t *testing.T) {
	server := newToolLoopServer("echo", `{}`)
	defer server.Close()

	registry := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echoes input", output: "ok"},
	})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, registry, 10, "", nil, 0)
	// No SetEventHandler — emission sites must be no-ops.
	if _, err := engine.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}
