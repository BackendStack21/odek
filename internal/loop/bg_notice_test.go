package loop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// captureServer records request bodies and replays scripted responses.
func captureServer(responses []string, bodies *[]string) *httptest.Server {
	var idx int
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(body))
		i := idx
		if i >= len(responses) {
			i = len(responses) - 1
		}
		idx++
		mu.Unlock()
		fmt.Fprint(w, responses[i])
	}))
}

// noopTool is a tool that always succeeds with "ok".
type noopTool struct{}

func (t *noopTool) Name() string        { return "noop" }
func (t *noopTool) Description() string { return "noop test tool" }
func (t *noopTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *noopTool) Call(args string) (string, error) { return "ok", nil }

// namedTool returns a tool with a custom name returning a fixed result.
type namedTool struct {
	name string
	out  string
}

func (t *namedTool) Name() string        { return t.name }
func (t *namedTool) Description() string { return t.name + " test tool" }
func (t *namedTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *namedTool) Call(args string) (string, error) { return t.out, nil }

func toolCallResp(name, args, id string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}],"finish_reason":"tool_calls"}`, id, name, args)
}

const finalResp = `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`

// TestBackgroundNotice_InjectedAsStandaloneUserMessage verifies the notice
// provider is drained at the top of each iteration and its output enters the
// context as its own user-role message — never spliced into the task message.
func TestBackgroundNotice_InjectedAsStandaloneUserMessage(t *testing.T) {
	var bodies []string
	server := captureServer([]string{toolCallResp("noop", "{}", "c1"), finalResp}, &bodies)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry([]tool.Tool{&noopTool{}})
	engine := New(client, registry, 10, "", nil, 0)

	calls := 0
	engine.SetBackgroundNoticeProvider(func() string {
		calls++
		if calls == 1 {
			return "[bg] job bg_x finished: exit 0"
		}
		return ""
	})

	_, _, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], "[bg] job bg_x finished") {
		t.Fatalf("first request missing notice:\n%s", bodies[0])
	}
	// Standalone: the notice must be its own user message object.
	if !strings.Contains(bodies[0], `"content":"[bg] job bg_x finished: exit 0"`) {
		t.Fatalf("notice not a standalone message:\n%s", bodies[0])
	}
	// Drain-once: the notice persists in history (chat is replayed per
	// request) but must appear exactly ONCE — no second injection.
	if got := strings.Count(bodies[1], "[bg] job bg_x finished"); got != 1 {
		t.Fatalf("notice occurrences in second request = %d, want 1 (drain-once violated):\n%s", got, bodies[1])
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (once per iteration)", calls)
	}
}

// TestBackgroundNotice_NilProvider verifies the loop runs unmodified when no
// provider is set (all non-bg surfaces).
func TestBackgroundNotice_NilProvider(t *testing.T) {
	var bodies []string
	server := captureServer([]string{finalResp}, &bodies)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry(nil)
	engine := New(client, registry, 10, "", nil, 0)

	_, _, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
}

// TestStallExempt_BGPollTools verifies bg_status/bg_output never trigger the
// stall correction even when called with identical arguments repeatedly —
// the whole point of polling is that the RESULT changes between calls.
func TestStallExempt_BGPollTools(t *testing.T) {
	responses := []string{
		toolCallResp("bg_status", `{"job_id":"bg_1"}`, "c1"),
		toolCallResp("bg_status", `{"job_id":"bg_1"}`, "c2"),
		toolCallResp("bg_status", `{"job_id":"bg_1"}`, "c3"),
		toolCallResp("bg_status", `{"job_id":"bg_1"}`, "c4"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry([]tool.Tool{&namedTool{name: "bg_status", out: `{"status":"running"}`}})
	engine := New(client, registry, 10, "", nil, 0)

	var recoveries []SignalEvent
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" {
			recoveries = append(recoveries, ev)
		}
	})

	_, _, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "user", Content: "poll it"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	for _, b := range bodies {
		if strings.Contains(b, "identical arguments") {
			t.Fatalf("stall correction fired for bg_status polling:\n%s", b)
		}
	}
	if len(recoveries) != 0 {
		t.Fatalf("tool_recovery signals = %d, want 0 for poll tools", len(recoveries))
	}
}

// TestStallStillFiresForOrdinaryTools is the control: the same repetition
// with a non-poll tool must still trigger the correction.
func TestStallStillFiresForOrdinaryTools(t *testing.T) {
	responses := []string{
		toolCallResp("noop", "{}", "c1"),
		toolCallResp("noop", "{}", "c2"),
		toolCallResp("noop", "{}", "c3"),
		finalResp,
	}
	var bodies []string
	server := captureServer(responses, &bodies)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry([]tool.Tool{&noopTool{}})
	engine := New(client, registry, 10, "", nil, 0)

	fired := false
	engine.SetSignalHandler(func(ev SignalEvent) {
		if ev.Type == "tool_recovery" {
			fired = true
		}
	})

	_, _, err := engine.RunWithMessages(context.Background(), []llm.Message{
		{Role: "user", Content: "loop it"},
	})
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if !fired {
		t.Fatal("control: tool_recovery must still fire for ordinary repeated tools")
	}
}
