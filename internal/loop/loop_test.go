package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/tool"
)

// fakeTool is a simple tool for testing.
type fakeTool struct {
	name        string
	description string
	output      string
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return f.description }
func (f *fakeTool) Schema() any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (f *fakeTool) Call(args string) (string, error) { return f.output, nil }

func TestEngine_Run_SimpleAnswer(t *testing.T) {
	// Fake server that returns a final answer immediately (no tool calls).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Hello from odek!"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	registry := tool.NewRegistry(nil)
	engine := New(client, registry, 10, "", nil, 0)

	result, err := engine.Run(context.Background(), "Say hello")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "Hello from odek!" {
		t.Errorf("result = %q, want %q", result, "Hello from odek!")
	}
}

func TestEngine_Run_ToolCallLoop(t *testing.T) {
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: model requests a tool
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"Let me check.",
						"tool_calls":[{
							"id":"call_1",
							"function":{
								"name":"echo",
								"arguments":"{\"text\":\"hello\"}"
							}
						}]
					}
				}]
			}`)
		} else {
			// Second call: final answer
			fmt.Fprint(w, `{"choices":[{"message":{"content":"The tool said: hello output"}}]}`)
		}
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echoes input", output: "hello output"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 10, "", nil, 0)

	result, err := engine.Run(context.Background(), "Echo hello")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "The tool said: hello output" {
		t.Errorf("result = %q, want %q", result, "The tool said: hello output")
	}
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}
}

func TestEngine_Run_MaxIterations(t *testing.T) {
	// Server that always requests a tool call, never gives a final answer.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[{
						"id":"call_1",
						"function":{
							"name":"echo",
							"arguments":"{}"
						}
					}]
				}
			}]
		}`)
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echo", output: "ok"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 3, "", nil, 0)

	_, err := engine.Run(context.Background(), "Loop forever")
	if err == nil {
		t.Fatal("expected max iterations error")
	}
}

func TestEngine_Run_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := engine.Run(ctx, "task")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestEngine_Run_SystemMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the system message is injected as the first message.
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if len(body.Messages) > 0 && body.Messages[0].Role == "system" {
				if body.Messages[0].Content != "You are a test bot." {
					t.Errorf("system message = %q, want %q", body.Messages[0].Content, "You are a test bot.")
				}
			} else {
				t.Error("system message not found or wrong role")
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, tool.NewRegistry(nil), 10, "You are a test bot.", nil, 0)

	result, err := engine.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

func TestEngine_Run_ToolNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[{
						"id":"call_x",
						"function":{
							"name":"nonexistent",
							"arguments":"{}"
						}
					}]
				}
			}]
		}`)
	}))
	defer server.Close()

	// No tools registered — the tool call will fail
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 0)

	// The loop should handle the missing tool gracefully — the tool error
	// is fed back to the model as a tool response message. The test server
	// only returns one response, so we'll hit max iterations.
	_, err := engine.Run(context.Background(), "use missing tool")
	if err == nil {
		t.Fatal("expected error (max iterations or similar)")
	}
}

func TestLastUserMessage_NoMessages(t *testing.T) {
	result := lastUserMessage(nil)
	if result != "" {
		t.Errorf("lastUserMessage(nil) = %q, want empty", result)
	}
	result = lastUserMessage([]llm.Message{})
	if result != "" {
		t.Errorf("lastUserMessage([]) = %q, want empty", result)
	}
}

func TestLastUserMessage_FindsLatest(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	}
	result := lastUserMessage(msgs)
	if result != "second" {
		t.Errorf("lastUserMessage = %q, want %q", result, "second")
	}
}

func TestEngine_RunWithMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"used RunWithMessages"}}],"usage":{"prompt_tokens":50,"completion_tokens":10}}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 0)

	msgs := []llm.Message{
		{Role: "system", Content: "bot"},
		{Role: "user", Content: "task"},
	}
	result, _, err := engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("RunWithMessages error: %v", err)
	}
	if result != "used RunWithMessages" {
		t.Errorf("result = %q, want %q", result, "used RunWithMessages")
	}
}

func TestEngine_RunWithMessages_TokenAccumulation(t *testing.T) {
	// Mock LLM that returns usage stats and triggers tool calls to
	// exercise multi-iteration accumulation.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount <= 2 {
			// Tool call responses with usage
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Step %d.","tool_calls":[{"id":"c_%d","function":{"name":"echo","arguments":"{}"}}]}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
				callCount, callCount, callCount*100, callCount*20)
		} else {
			// Final answer with usage
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done."}}],"usage":{"prompt_tokens":500,"completion_tokens":50}}`)
		}
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	registry := tool.NewRegistry([]tool.Tool{&fakeTool{name: "echo", description: "echo", output: "pong"}})
	engine := New(client, registry, 10, "", nil, 0)

	msgs := []llm.Message{
		{Role: "system", Content: "bot"},
		{Role: "user", Content: "do it"},
	}

	_, _, err := engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("RunWithMessages error: %v", err)
	}

	// Iteration tokens: iter1=100/20, iter2=200/40, iter3=500/50
	wantInput := 100 + 200 + 500   // 800
	wantOutput := 20 + 40 + 50      // 110

	if engine.TotalInputTokens != wantInput {
		t.Errorf("TotalInputTokens = %d, want %d", engine.TotalInputTokens, wantInput)
	}
	if engine.TotalOutputTokens != wantOutput {
		t.Errorf("TotalOutputTokens = %d, want %d", engine.TotalOutputTokens, wantOutput)
	}

	// Verify token fields reset on a second call (not cumulative)
	callCount = 0
	engine.RunWithMessages(context.Background(), msgs)
	// After reset, should be 800 again (same pattern), NOT 1600 (cumulative)
	if engine.TotalInputTokens != 800 {
		t.Errorf("TotalInputTokens after reset = %d, want 800 (not cumulative across calls)", engine.TotalInputTokens)
	}
	if engine.TotalOutputTokens != 110 {
		t.Errorf("TotalOutputTokens after reset = %d, want 110 (not cumulative)", engine.TotalOutputTokens)
	}
}

func TestEngine_BuildToolDefs(t *testing.T) {
	t1 := &fakeTool{name: "read", description: "read files"}
	t2 := &fakeTool{name: "write", description: "write files"}
	registry := tool.NewRegistry([]tool.Tool{t1, t2})

	engine := New(nil, registry, 10, "", nil, 0)
	defs := engine.buildToolDefs()

	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		if d.Type != "function" {
			t.Errorf("ToolDef.Type = %q, want %q", d.Type, "function")
		}
		names[d.Function.Name] = true
	}

	if !names["read"] || !names["write"] {
		t.Errorf("missing expected tool names: got %v", names)
	}
}

func TestEngine_BuildToolDefs_StringSchema(t *testing.T) {
	// Test the string schema path in buildToolDefs
	st := &stringSchemaTool{name: "custom", description: "custom tool", schemaStr: `{"type":"object"}`}
	registry := tool.NewRegistry([]tool.Tool{st})

	engine := New(nil, registry, 10, "", nil, 0)
	defs := engine.buildToolDefs()

	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(defs))
	}
	if defs[0].Function.Name != "custom" {
		t.Errorf("name = %q, want 'custom'", defs[0].Function.Name)
	}
}

func TestEngine_BuildToolDefs_EmptyStringSchema(t *testing.T) {
	st := &stringSchemaTool{name: "empty", description: "empty", schemaStr: ""}
	registry := tool.NewRegistry([]tool.Tool{st})

	engine := New(nil, registry, 10, "", nil, 0)
	defs := engine.buildToolDefs()

	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(defs))
	}
	// Empty string schema should produce empty properties object
}

// stringSchemaTool returns Schema() as a string instead of map[string]any
type stringSchemaTool struct {
	name        string
	description string
	schemaStr   string
}

func (s *stringSchemaTool) Name() string                     { return s.name }
func (s *stringSchemaTool) Description() string              { return s.description }
func (s *stringSchemaTool) Schema() any                      { return s.schemaStr }
func (s *stringSchemaTool) Call(args string) (string, error) { return "ok", nil }

// Test context cancellation inside the iteration loop (not before start).
func TestEngine_Run_ContextCancelDuringLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cancel context during the first LLM call. The loop processes
		// the tool call synchronously, then on the next iteration
		// ctx.Done() fires.
		cancel()
		fmt.Fprint(w, `{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[{
						"id":"call_1",
						"function":{
							"name":"echo",
							"arguments":"{}"
						}
					}]
				}
			}]
		}`)
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echo", output: "ok"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 10, "", nil, 0)

	_, err := engine.Run(ctx, "task")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// Test the path where tool.Call() returns an error (lines 74-75 in loop.go).
func TestEngine_Run_ToolCallError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[{
						"id":"call_1",
						"function":{
							"name":"failing",
							"arguments":"{}"
						}
					}]
				}
			}]
		}`)
	}))
	defer server.Close()

	failingTool := &errorTool{name: "failing", description: "always fails"}
	registry := tool.NewRegistry([]tool.Tool{failingTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 10, "", nil, 0)

	// Tool error is fed back as a tool response; server only returns one
	// response, so we hit max iterations.
	_, err := engine.Run(context.Background(), "use failing tool")
	if err == nil {
		t.Fatal("expected error (max iterations)")
	}
}

// errorTool returns an error from Call().
type errorTool struct {
	name        string
	description string
}

func (e *errorTool) Name() string                     { return e.name }
func (e *errorTool) Description() string              { return e.description }
func (e *errorTool) Schema() any                      { return map[string]any{"type": "object"} }
func (e *errorTool) Call(args string) (string, error) { return "", fmt.Errorf("tool error") }

// ═════════════════════════════════════════════════════════════════════
// Context Trimming Tests
// ═════════════════════════════════════════════════════════════════════

func TestEstimateTokens_Empty(t *testing.T) {
	if n := estimateTokens(""); n != 0 {
		t.Errorf("estimateTokens('') = %d, want 0", n)
	}
}

func TestEstimateTokens_Short(t *testing.T) {
	// "hello" is 5 chars → (5+3)/4 = 2 tokens (conservative overestimate)
	if n := estimateTokens("hello"); n != 2 {
		t.Errorf("estimateTokens('hello') = %d, want 2", n)
	}
}

func TestEstimateTokens_Long(t *testing.T) {
	// ~4 chars per token — 1000 chars should be ~250 tokens
	n := estimateTokens(strings.Repeat("x", 1000))
	if n < 200 || n > 300 {
		t.Errorf("estimateTokens(1000 chars) = %d, want ~250", n)
	}
}

func TestEstimateMessages_Empty(t *testing.T) {
	if n := estimateMessages(nil); n != 0 {
		t.Errorf("estimateMessages(nil) = %d, want 0", n)
	}
}

func TestEstimateMessages_Single(t *testing.T) {
	msg := []llm.Message{{Role: "user", Content: "hello"}}
	n := estimateMessages(msg)
	// 50 overhead + 2 tokens for "hello" = 52
	if n < 50 || n > 55 {
		t.Errorf("estimateMessages(single) = %d, want ~52", n)
	}
}

func TestEstimateMessages_WithToolCalls(t *testing.T) {
	msg := []llm.Message{{
		Role:    "assistant",
		Content: "Let me check",
		ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "shell", Arguments: `{"cmd":"ls"}`},
		}},
	}}
	n := estimateMessages(msg)
	if n < 80 {
		t.Errorf("estimateMessages(with tool call) = %d, want >80", n)
	}
}

func TestContextBudget_NoLimit(t *testing.T) {
	if n := contextBudget(0); n != 0 {
		t.Errorf("contextBudget(0) = %d, want 0", n)
	}
}

func TestContextBudget_WithLimit(t *testing.T) {
	// 131072 * 0.75 = 98304
	if n := contextBudget(131072); n != 98304 {
		t.Errorf("contextBudget(131072) = %d, want 98304", n)
	}
}

func TestTrimContext_NoLimit(t *testing.T) {
	engine := &Engine{maxContext: 0}
	msgs := []llm.Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "user", Content: "hello"},
	}
	result := engine.trimContext(msgs, nil)
	if len(result) != 2 {
		t.Errorf("trimContext with no limit should not change messages, got %d", len(result))
	}
}

func TestTrimContext_UnderBudget(t *testing.T) {
	// Large budget — messages fit easily
	engine := &Engine{maxContext: 1_000_000}
	msgs := []llm.Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Hi there", ToolCalls: nil},
		{Role: "tool", Content: "result", ToolCallID: "call_1"},
	}
	result := engine.trimContext(msgs, nil)
	if len(result) != 4 {
		t.Errorf("trimContext under budget should keep all messages, got %d", len(result))
	}
}

func TestTrimContext_OverBudget(t *testing.T) {
	// Very tight budget — forces trimming
	engine := &Engine{maxContext: 200}
	msgs := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant. Be concise."},
		{Role: "user", Content: "Explain how the quantum fourier transform works in detail"},
		{Role: "assistant", Content: strings.Repeat("thinking about this... ", 20)},
		{Role: "tool", Content: strings.Repeat("some result data ", 20), ToolCallID: "call_1"},
		{Role: "assistant", Content: strings.Repeat("more reasoning... ", 20)},
		{Role: "tool", Content: strings.Repeat("more data ", 20), ToolCallID: "call_2"},
		{Role: "assistant", Content: strings.Repeat("final reasoning... ", 20)},
		{Role: "tool", Content: strings.Repeat("final data ", 20), ToolCallID: "call_3"},
	}
	result := engine.trimContext(msgs, nil)

	// Should have preserved system + task (first user)
	if len(result) < 2 {
		t.Errorf("trimContext should keep at least system + task, got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Errorf("trimContext should keep system message first, got role=%q", result[0].Role)
	}
	if result[1].Role != "user" {
		t.Errorf("trimContext should keep task message second, got role=%q", result[1].Role)
	}

	// Should have fewer messages than original
	if len(result) >= len(msgs) {
		t.Errorf("trimContext should reduce messages, got %d >= %d", len(result), len(msgs))
	}
}

func TestTrimContext_VeryTightBudget(t *testing.T) {
	// Extremely tight budget — still should keep system + task
	engine := &Engine{maxContext: 100}
	msgs := []llm.Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "user", Content: "Hello world, this is a task message that is somewhat long"},
		{Role: "assistant", Content: strings.Repeat("data ", 50)},
		{Role: "tool", Content: strings.Repeat("result ", 50), ToolCallID: "call_1"},
	}
	result := engine.trimContext(msgs, nil)

	// Must keep system + task at minimum
	if len(result) < 2 {
		t.Errorf("trimContext(VeryTight) should keep system + task, got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Errorf("trimContext(VeryTight) should keep system first")
	}
	if result[1].Role != "user" {
		t.Errorf("trimContext(VeryTight) should keep task second, got %q", result[1].Role)
	}
}

func TestTrimContext_NoSystemMessage(t *testing.T) {
	engine := &Engine{maxContext: 150}
	msgs := []llm.Message{
		{Role: "user", Content: "This is a long task message that takes up many tokens"},
		{Role: "assistant", Content: strings.Repeat("data ", 30)},
		{Role: "tool", Content: strings.Repeat("result ", 30), ToolCallID: "call_1"},
	}
	result := engine.trimContext(msgs, nil)

	// Without system, keep at least the task
	if len(result) < 1 {
		t.Errorf("trimContext(no system) should keep task, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("trimContext(no system) should keep task first, got %q", result[0].Role)
	}
}

func TestEstimateToolDefs_Empty(t *testing.T) {
	if n := estimateToolDefs(nil); n != 0 {
		t.Errorf("estimateToolDefs(nil) = %d, want 0", n)
	}
}

func TestEstimateToolDefs_Single(t *testing.T) {
	defs := []llm.ToolDef{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "shell",
			Description: "run a shell command",
		},
	}}
	n := estimateToolDefs(defs)
	if n < 30 {
		t.Errorf("estimateToolDefs(single) = %d, want >30", n)
	}
}

func TestTrimContext_IncludesToolDefTokens(t *testing.T) {
	// Budget that forces trimming when tool defs are included
	engine := &Engine{maxContext: 300}
	msgs := []llm.Message{
		{Role: "system", Content: "You are a bot."},
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: strings.Repeat("long thinking ", 30)},
		{Role: "tool", Content: strings.Repeat("long result ", 30), ToolCallID: "call_1"},
	}
	defs := []llm.ToolDef{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "shell",
			Description: strings.Repeat("very long description that takes up tokens ", 10),
		},
	}}

	result := engine.trimContext(msgs, defs)
	if len(result) >= len(msgs) {
		t.Errorf("trimContext with tool defs should trim, got %d >= %d", len(result), len(msgs))
	}
}

func TestEngine_SkillLoader_CalledOncePerInput(t *testing.T) {
	// Regression: SkillLoader must fire only once per unique user message,
	// not once per iteration. Verifies the skill injection leak fix.
	skillLoadCount := 0
	var loadedInput string

	skillLoader := func(userInput string) string {
		skillLoadCount++
		loadedInput = userInput
		return "injected skill content"
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First iteration: request a tool call
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"Let me think.",
						"tool_calls":[{
							"id":"call_1",
							"function":{
								"name":"echo",
								"arguments":"{}"
							}
						}]
					}
				}]
			}`)
		} else {
			// Second iteration: final answer
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echo", output: "ok"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetSkillLoader(skillLoader)

	result, err := engine.Run(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want %q", result, "done")
	}

	// SkillLoader should have been called exactly once,
	// not once per iteration (which would be 2+)
	if skillLoadCount != 1 {
		t.Errorf("SkillLoader called %d times, want 1 (should dedup per input)", skillLoadCount)
	}
	if loadedInput != "do the task" {
		t.Errorf("loadedInput = %q, want %q", loadedInput, "do the task")
	}
	if callCount != 2 {
		t.Errorf("LLM called %d times, want 2", callCount)
	}
}

func TestEngine_ToolEventHandler(t *testing.T) {
	// Verify that ToolEventHandler fires tool_call before and tool_result
	// after each tool invocation, and does so live (during the loop).
	var events []string
	var eventData []string
	eventHandler := func(event, name, data string) {
		events = append(events, event)
		eventData = append(eventData, name)
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First iteration: request a tool call
			fmt.Fprint(w, `{
				"choices":[{
					"message":{
						"content":"Checking.",
						"tool_calls":[{
							"id":"call_1",
							"function":{
								"name":"echo",
								"arguments":"{}"
							}
						}]
					}
				}]
			}`)
		} else {
			// Final answer
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()

	echoTool := &fakeTool{name: "echo", description: "echo", output: "ok"}
	registry := tool.NewRegistry([]tool.Tool{echoTool})
	client := llm.New(server.URL, "sk-test", "test-model", "", 0)
	engine := New(client, registry, 10, "", nil, 0)
	engine.SetToolEventHandler(eventHandler)

	result, err := engine.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want %q", result, "done")
	}

	// Must have exactly: tool_call → tool_result
	if len(events) != 2 {
		t.Fatalf("expected 2 events (tool_call, tool_result), got %d: %v", len(events), events)
	}
	if events[0] != "tool_call" {
		t.Errorf("event[0] = %q, want 'tool_call'", events[0])
	}
	if events[1] != "tool_result" {
		t.Errorf("event[1] = %q, want 'tool_result'", events[1])
	}
	if eventData[0] != "echo" {
		t.Errorf("event[0] name = %q, want 'echo'", eventData[0])
	}
	if eventData[1] != "echo" {
		t.Errorf("event[1] name = %q, want 'echo'", eventData[1])
	}
}
