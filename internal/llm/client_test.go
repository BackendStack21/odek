package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCallParamsMarshaling_NoThinking(t *testing.T) {
	body := CallParams{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
		Stream: false,
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	// Thinking field should be absent (omitempty)
	if _, ok := result["thinking"]; ok {
		t.Error("thinking field should be absent when not set")
	}
	if _, ok := result["reasoning_effort"]; ok {
		t.Error("reasoning_effort field should be absent when not set")
	}
}

func TestCallParamsMarshaling_ThinkingEnabled(t *testing.T) {
	body := CallParams{
		Model:    "deepseek-chat",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Stream:   false,
		Thinking: &ThinkingConfig{Type: "enabled"},
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	thinking, ok := result["thinking"]
	if !ok {
		t.Fatal("thinking field should be present when set")
	}
	thinkingMap, ok := thinking.(map[string]any)
	if !ok {
		t.Fatal("thinking field should be an object")
	}
	if thinkingMap["type"] != "enabled" {
		t.Errorf("thinking.type = %q, want %q", thinkingMap["type"], "enabled")
	}
}

func TestCallParamsMarshaling_ThinkingDisabled(t *testing.T) {
	body := CallParams{
		Model:    "deepseek-chat",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Stream:   false,
		Thinking: &ThinkingConfig{Type: "disabled"},
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	thinking, ok := result["thinking"]
	if !ok {
		t.Fatal("thinking field should be present when set")
	}
	thinkingMap := thinking.(map[string]any)
	if thinkingMap["type"] != "disabled" {
		t.Errorf("thinking.type = %q, want %q", thinkingMap["type"], "disabled")
	}
}

func TestCallParamsMarshaling_ReasoningEffort(t *testing.T) {
	tests := []string{"low", "medium", "high"}

	for _, level := range tests {
		body := CallParams{
			Model:           "o1",
			Messages:        []Message{{Role: "user", Content: "hello"}},
			Stream:          false,
			ReasoningEffort: level,
		}

		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}

		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}

		effort, ok := result["reasoning_effort"]
		if !ok {
			t.Errorf("reasoning_effort should be present for %q", level)
			continue
		}
		if effort != level {
			t.Errorf("reasoning_effort = %q, want %q", effort, level)
		}
	}
}

func TestParseResponse_ContentOnly(t *testing.T) {
	raw := `{
		"choices": [{
			"message": {
				"content": "Hello, world!"
			}
		}]
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello, world!")
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(result.ToolCalls))
	}
}

func TestParseResponse_ToolCalls(t *testing.T) {
	raw := `{
		"choices": [{
			"message": {
				"content": null,
				"tool_calls": [{
					"id": "call_123",
					"function": {
						"name": "shell",
						"arguments": "{\"command\":\"ls\"}"
					}
				}]
			}
		}]
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "" {
		t.Errorf("Content should be empty, got %q", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_123")
	}
	if tc.Function.Name != "shell" {
		t.Errorf("ToolCall.Function.Name = %q, want %q", tc.Function.Name, "shell")
	}
	if tc.Function.Arguments != `{"command":"ls"}` {
		t.Errorf("ToolCall.Function.Arguments = %q, want %q", tc.Function.Arguments, `{"command":"ls"}`)
	}
}

func TestParseResponse_ContentAndToolCalls(t *testing.T) {
	raw := `{
		"choices": [{
			"message": {
				"content": "Let me check that file.",
				"tool_calls": [{
					"id": "call_456",
					"function": {
						"name": "shell",
						"arguments": "{\"command\":\"cat file.txt\"}"
					}
				}]
			}
		}]
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Let me check that file." {
		t.Errorf("Content = %q, want %q", result.Content, "Let me check that file.")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Function.Name != "shell" {
		t.Errorf("ToolCall name = %q, want %q", result.ToolCalls[0].Function.Name, "shell")
	}
}

func TestParseResponse_EmptyChoices(t *testing.T) {
	raw := `{"choices": []}`

	_, err := parseResponse([]byte(raw))
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestParseResponse_InvalidJSON(t *testing.T) {
	_, err := parseResponse([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCallParamsMarshaling_WithTools(t *testing.T) {
	body := CallParams{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: "user", Content: "list files"},
		},
		Tools: []ToolDef{
			{
				Type: "function",
				Function: FunctionDef{
					Name:        "shell",
					Description: "Run a command",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"command": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		Stream: false,
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	tools, ok := result["tools"]
	if !ok {
		t.Fatal("tools field should be present")
	}
	toolsArr, ok := tools.([]any)
	if !ok || len(toolsArr) != 1 {
		t.Fatalf("expected 1 tool, got %v", tools)
	}
}

func TestClient_ThinkingSwitch(t *testing.T) {
	tests := []struct {
		name         string
		thinking     string
		expectThink  bool
		expectReason bool
	}{
		{"enabled", "enabled", true, false},
		{"disabled", "disabled", true, false},
		{"low", "low", false, true},
		{"medium", "medium", false, true},
		{"high", "high", false, true},
		{"empty", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate what Call() does — construct the same body
			body := CallParams{
				Model:    "test-model",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Stream:   false,
			}

			switch tt.thinking {
			case "enabled", "disabled":
				body.Thinking = &ThinkingConfig{Type: tt.thinking}
			case "low", "medium", "high":
				body.ReasoningEffort = tt.thinking
			}

			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}

			var result map[string]any
			json.Unmarshal(data, &result)

			_, hasThinking := result["thinking"]
			_, hasReasoning := result["reasoning_effort"]

			if hasThinking != tt.expectThink {
				t.Errorf("thinking field present = %v, want %v", hasThinking, tt.expectThink)
			}
			if hasReasoning != tt.expectReason {
				t.Errorf("reasoning_effort present = %v, want %v", hasReasoning, tt.expectReason)
			}
		})
	}
}

func TestClient_New(t *testing.T) {
	c := New("https://api.example.com/v1", "sk-key", "gpt-4", "enabled", 0, 0)
	if c.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.APIKey != "sk-key" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
	if c.Model != "gpt-4" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.Thinking != "enabled" {
		t.Errorf("Thinking = %q", c.Thinking)
	}
}

func TestClient_New_TrailingSlash(t *testing.T) {
	c := New("https://api.example.com/v1/", "sk-key", "model", "", 0, 0)
	if c.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL should trim trailing slash, got %q", c.BaseURL)
	}
}

func TestClient_New_CustomTimeout(t *testing.T) {
	c := New("https://api.example.com", "sk-key", "model", "", 0, 30*time.Second)
	if c.http.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestClient_New_ZeroTimeoutUsesDefault(t *testing.T) {
	c := New("https://api.example.com", "sk-key", "model", "", 0, 0)
	if c.http.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, want 120s", c.http.Timeout)
	}
}

func TestClient_Call_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}
}

func TestClient_Call_HTTPError(t *testing.T) {
	stubRetrySleep(t) // 500 is retryable — full exhaustion without the stub takes ~90s
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClient_Call_WithThinking(t *testing.T) {
	// DeepSeek supports the Anthropic-style thinking object natively.
	c := New("https://api.deepseek.com/v1", "sk-test", "deepseek-chat", "enabled", 0, 0)
	body := c.buildCallParams([]Message{{Role: "user", Content: "think"}}, nil, nil)
	if body.Thinking == nil || body.Thinking.Type != "enabled" {
		t.Errorf("thinking = %v, want {type: enabled}", body.Thinking)
	}
}

func TestClient_Call_WithReasoningEffort(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"reasoned"}}]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "o1", "high", 0, 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "reason"}}, nil, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Content != "reasoned" {
		t.Errorf("Content = %q", result.Content)
	}
	effort, ok := receivedBody["reasoning_effort"]
	if !ok || effort != "high" {
		t.Errorf("reasoning_effort = %v, want 'high'", effort)
	}
}

func TestClient_Call_InvalidEndpoint(t *testing.T) {
	stubRetrySleep(t) // connection refused is retryable — stub the backoff
	c := New("http://127.0.0.1:1", "sk-test", "model", "", 0, 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// Test Call() with tools passed in the request body.
func TestClient_Call_WithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"used tool"}}]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	tools := []ToolDef{
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "shell",
				Description: "run a command",
				Parameters:  map[string]any{"type": "object"},
			},
		},
	}
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, tools)
	if err != nil {
		t.Fatalf("Call() with tools error: %v", err)
	}
	if result.Content != "used tool" {
		t.Errorf("Content = %q, want %q", result.Content, "used tool")
	}
}

// Test Call() with a 401 Unauthorized response.
func TestClient_Call_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-bad", "test-model", "", 0, 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

// Test Call() with invalid JSON in the response body. Malformed 200 bodies
// are retried (transient gateway artifact), so the error surfaces only after
// the full retry budget is spent.
func TestClient_Call_InvalidJSONResponse(t *testing.T) {
	stubRetrySleep(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestParseResponse_WithUsage(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "Hello"}}],
		"usage": {"prompt_tokens": 452, "completion_tokens": 128, "total_tokens": 580}
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 452 {
		t.Errorf("InputTokens = %d, want 452", result.InputTokens)
	}
	if result.OutputTokens != 128 {
		t.Errorf("OutputTokens = %d, want 128", result.OutputTokens)
	}
	if result.Content != "Hello" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello")
	}
}

func TestParseResponse_WithoutUsage(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "No usage"}}]
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", result.InputTokens)
	}
	if result.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", result.OutputTokens)
	}
}

func TestParseResponse_UsageWithToolCalls(t *testing.T) {
	raw := `{
		"choices": [{
			"message": {
				"content": "Let me check.",
				"tool_calls": [{
					"id": "call_1",
					"function": {"name": "shell", "arguments": "{\"cmd\":\"ls\"}"}
				}]
			}
		}],
		"usage": {"prompt_tokens": 1000, "completion_tokens": 50, "total_tokens": 1050}
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", result.InputTokens)
	}
	if result.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", result.OutputTokens)
	}
	if len(result.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}

func TestClient_Call_ReturnsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60}}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", result.InputTokens)
	}
	if result.OutputTokens != 10 {
		t.Errorf("OutputTokens = %d, want 10", result.OutputTokens)
	}
}

func TestClient_SimpleCall_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"simple response"}}]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	result, err := c.SimpleCall(context.Background(), "You are a bot.", "say hi")
	if err != nil {
		t.Fatalf("SimpleCall() error: %v", err)
	}
	if result != "simple response" {
		t.Errorf("result = %q, want %q", result, "simple response")
	}
}

func TestClient_SimpleCall_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	_, err := c.SimpleCall(context.Background(), "bot", "hi")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestClient_SimpleCall_EmptyResponse(t *testing.T) {
	stubRetrySleep(t) // zero-choices 200 is retried — stub the backoff
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0, 0)
	_, err := c.SimpleCall(context.Background(), "bot", "hi")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

// ── DeepSeek v4 Flash Model Validation ────────────────────────────────

// TestClient_Call_FlashModelNoThinkingField validates that when using
// deepseek-v4-flash (which has no DefaultThinking), the request body
// does NOT include a "thinking" field. Flash is faster/cheaper by
// skipping extended reasoning — this test guards against accidentally
// sending thinking config to Flash.
func TestClient_Call_FlashModelNoThinkingField(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"flash response"}}]}`))
	}))
	defer server.Close()

	// Flash: model=deepseek-v4-flash, thinking="" (the default)
	c := New(server.URL, "sk-test", "deepseek-v4-flash", "", 0, 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Flash Call() error: %v", err)
	}
	if result.Content != "flash response" {
		t.Errorf("Content = %q, want %q", result.Content, "flash response")
	}

	// Verify model name is correct in the request
	model, ok := receivedBody["model"]
	if !ok || model != "deepseek-v4-flash" {
		t.Errorf("model = %v, want %q", model, "deepseek-v4-flash")
	}

	// Verify NO thinking field (Flash doesn't use extended thinking)
	if _, ok := receivedBody["thinking"]; ok {
		t.Error("Flash request should NOT contain 'thinking' field")
	}
	if _, ok := receivedBody["reasoning_effort"]; ok {
		t.Error("Flash request should NOT contain 'reasoning_effort' field")
	}
}

// TestClient_Call_FlashVsProThinkingContrast validates that Flash and Pro
// models are handled differently at the HTTP level:
//   - Flash: no thinking field (faster, cheaper)
//   - Pro:   thinking{type:"enabled"} by default (full reasoning)
func TestClient_Call_FlashVsProThinkingContrast(t *testing.T) {
	t.Run("flash_no_thinking", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}))
		defer server.Close()

		c := New(server.URL, "sk-test", "deepseek-v4-flash", "", 0, 0)
		_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := body["thinking"]; ok {
			t.Error("Flash: thinking field should be absent")
		}
	})

	t.Run("pro_thinking_enabled", func(t *testing.T) {
		// DeepSeek supports the Anthropic-style thinking object natively.
		c := New("https://api.deepseek.com/v1", "sk-test", "deepseek-v4-pro", "enabled", 0, 0)
		body := c.buildCallParams([]Message{{Role: "user", Content: "hi"}}, nil, nil)
		if body.Thinking == nil {
			t.Fatal("Pro: thinking field should be present")
		}
		if body.Thinking.Type != "enabled" {
			t.Errorf("Pro: thinking.type = %v, want 'enabled'", body.Thinking.Type)
		}
	})
}

func TestParseResponse_AnthropicCacheMetrics(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "cached response"}}],
		"usage": {
			"prompt_tokens": 500,
			"completion_tokens": 50,
			"cache_creation_input_tokens": 400,
			"cache_read_input_tokens": 100
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheCreationTokens != 400 {
		t.Errorf("CacheCreationTokens = %d, want 400", result.CacheCreationTokens)
	}
	if result.CacheReadTokens != 100 {
		t.Errorf("CacheReadTokens = %d, want 100", result.CacheReadTokens)
	}
}

func TestParseResponse_OpenAICacheMetrics(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "openai response"}}],
		"usage": {
			"prompt_tokens": 300,
			"completion_tokens": 30,
			"prompt_tokens_details": {"cached_tokens": 200}
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CachedTokens != 200 {
		t.Errorf("CachedTokens = %d, want 200", result.CachedTokens)
	}
}

func TestParseResponse_DeepSeekCacheMetrics(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "deepseek response"}}],
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 40,
			"prompt_cache_hit_tokens": 750,
			"prompt_cache_miss_tokens": 250
		}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheReadTokens != 750 {
		t.Errorf("CacheReadTokens = %d, want 750 (deepseek hit)", result.CacheReadTokens)
	}
	if result.CacheCreationTokens != 250 {
		t.Errorf("CacheCreationTokens = %d, want 250 (deepseek miss)", result.CacheCreationTokens)
	}
	if !result.CacheReported {
		t.Error("CacheReported should be true when deepseek cache fields are present")
	}
}

func TestParseResponse_CacheNotReported(t *testing.T) {
	raw := `{
		"choices": [{"message": {"content": "plain response"}}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 10}
	}`
	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheReported {
		t.Error("CacheReported should be false when no cache fields are present")
	}
}

func TestApplyCacheMarkers_WithSystemPrompt(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "List the files."},
	}

	annotated, system := ApplyCacheMarkers(messages)

	for _, m := range annotated {
		if m.Role == "system" {
			t.Error("system message should be removed from messages array")
		}
	}

	if len(system) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(system))
	}
	if system[0].Type != "text" {
		t.Errorf("SystemBlock.Type = %q, want 'text'", system[0].Type)
	}
	if system[0].Text != "You are a helpful assistant." {
		t.Errorf("SystemBlock.Text = %q", system[0].Text)
	}
	if system[0].CacheControl == nil || system[0].CacheControl.Type != "ephemeral" {
		t.Error("system block should have cache_control: ephemeral")
	}

	if len(annotated) != 1 {
		t.Fatalf("expected 1 message, got %d", len(annotated))
	}
	if annotated[0].CacheControl == nil || annotated[0].CacheControl.Type != "ephemeral" {
		t.Error("first user message should have cache_control: ephemeral")
	}
}

func TestApplyCacheMarkers_NoSystemPrompt(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "Hello!"},
	}

	annotated, system := ApplyCacheMarkers(messages)

	if len(system) != 0 {
		t.Errorf("expected 0 system blocks, got %d", len(system))
	}
	if len(annotated) != 1 {
		t.Fatalf("expected 1 message, got %d", len(annotated))
	}
	if annotated[0].CacheControl == nil || annotated[0].CacheControl.Type != "ephemeral" {
		t.Error("first user message should have cache_control: ephemeral")
	}
}

func TestApplyCacheMarkers_OnlyFirstUserGetsMarker(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "first request"},
		{Role: "assistant", Content: "thinking..."},
		{Role: "user", Content: "follow-up"},
	}

	annotated, _ := ApplyCacheMarkers(messages)

	if len(annotated) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(annotated))
	}

	if annotated[0].CacheControl == nil || annotated[0].CacheControl.Type != "ephemeral" {
		t.Error("first user message should have cache_control: ephemeral")
	}

	if annotated[2].CacheControl != nil {
		t.Error("second user message should NOT have cache_control")
	}
}

func TestCallParamsMarshaling_WithSystemField(t *testing.T) {
	body := CallParams{
		Model: "claude-sonnet-4",
		Messages: []Message{
			{Role: "user", Content: "hello", CacheControl: &CacheControl{Type: "ephemeral"}},
		},
		System: []SystemBlock{
			{Type: "text", Text: "system prompt", CacheControl: &CacheControl{Type: "ephemeral"}},
		},
		MaxTokens: 4096,
		Stream:    false,
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	if v, ok := result["max_tokens"]; !ok || v != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096", v)
	}

	sys, ok := result["system"]
	if !ok {
		t.Fatal("system field should be present")
	}
	sysArr, ok := sys.([]any)
	if !ok || len(sysArr) != 1 {
		t.Fatalf("system should be an array with 1 element, got %v", sys)
	}
	sysMap := sysArr[0].(map[string]any)
	if sysMap["type"] != "text" {
		t.Errorf("system[0].type = %q", sysMap["type"])
	}
	if sysMap["text"] != "system prompt" {
		t.Errorf("system[0].text = %q", sysMap["text"])
	}

	msgs := result["messages"].([]any)
	firstMsg := msgs[0].(map[string]any)
	cc, ok := firstMsg["cache_control"]
	if !ok {
		t.Fatal("first message should have cache_control")
	}
	ccMap := cc.(map[string]any)
	if ccMap["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %q", ccMap["type"])
	}
}

func TestCallParamsMarshaling_SystemOmitEmpty(t *testing.T) {
	body := CallParams{
		Model:    "deepseek-chat",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	json.Unmarshal(data, &result)

	if _, ok := result["system"]; ok {
		t.Error("system field should be omitted when empty")
	}
	if _, ok := result["max_tokens"]; ok {
		t.Error("max_tokens should be omitted when 0")
	}
}

func TestClient_NewWithMaxTokens(t *testing.T) {
	c := NewWithMaxTokens("https://api.example.com", "sk-key", "model", "", 0, 8192, 0)
	if c.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", c.MaxTokens)
	}
	if c.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
}

func TestParseResponse_NoCacheMetrics(t *testing.T) {
	raw := `{
		"choices": [{
			"message": {
				"content": "No cache"
			}
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 20
		}
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want 0", result.CacheCreationTokens)
	}
	if result.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0", result.CacheReadTokens)
	}
	if result.CachedTokens != 0 {
		t.Errorf("CachedTokens = %d, want 0", result.CachedTokens)
	}
}

func TestParseResponse_AnthropicAndOpenAICache(t *testing.T) {
	// Both Anthropic and OpenAI cache fields present — Anthropic takes precedence.
	raw := `{
		"choices": [{
			"message": {
				"content": "Both"
			}
		}],
		"usage": {
			"prompt_tokens": 300,
			"completion_tokens": 60,
			"cache_creation_input_tokens": 70,
			"cache_read_input_tokens": 140,
			"prompt_tokens_details": {
				"cached_tokens": 999
			}
		}
	}`

	result, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheCreationTokens != 70 {
		t.Errorf("CacheCreationTokens = %d, want 70", result.CacheCreationTokens)
	}
	if result.CacheReadTokens != 140 {
		t.Errorf("CacheReadTokens = %d, want 140", result.CacheReadTokens)
	}
	if result.CachedTokens != 999 {
		t.Errorf("CachedTokens = %d, want 999", result.CachedTokens)
	}
}

func TestClient_IsAnthropic(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.anthropic.com/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://api.deepseek.com/v1", false},
		{"http://localhost:11434/v1", false},
	}
	for _, tc := range cases {
		c := New(tc.baseURL, "sk-test", "model", "", 0, 0)
		if got := c.IsAnthropic(); got != tc.want {
			t.Errorf("IsAnthropic(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

// OpenAI reasoning models (o1/o3/o4, gpt-5 family) and Kimi Code models
// (kimi-for-coding*, k3*) reject any explicit temperature other than the
// default (1) with a 400. The client must omit the field for those models
// while still sending odek's deterministic default (0) to models that
// accept it.
func TestCall_OmitsTemperatureForReasoningModels(t *testing.T) {
	cases := []struct {
		model        string
		wantTempSent bool
	}{
		{"gpt-5-nano", false},
		{"gpt-5", false},
		{"o3-mini", false},
		{"o1-preview", false},
		{"o4-mini", false},
		{"GPT-5-MINI", false}, // case-insensitive
		{"kimi-for-coding", false},
		{"kimi-for-coding-highspeed", false},
		{"k3", false},
		{"k3-256k", false},
		{"Kimi-For-Coding", false}, // case-insensitive
		{"gpt-4o-mini", true},
		{"deepseek-chat", true},
		{"claude-sonnet-4-5", true},
		{"kimi-latest", true}, // Moonshot platform models accept temperature
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			var captured []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			}))
			defer server.Close()

			c := New(server.URL, "sk-test", tc.model, "", 0, 0)
			c.Temperature = 0 // odek's deterministic default
			if _, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
				t.Fatalf("Call: %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(captured, &body); err != nil {
				t.Fatalf("captured request is not JSON: %v", err)
			}
			_, sent := body["temperature"]
			if sent != tc.wantTempSent {
				t.Errorf("model %q: temperature sent = %v, want %v (body: %s)", tc.model, sent, tc.wantTempSent, captured)
			}
		})
	}
}

// Models like gpt-5.6-luna reject function tools combined with any
// reasoning_effort other than "none" — and their default effort is not
// "none", so omitting the field still 400s. The client must learn the
// constraint from the 400, retry with reasoning_effort "none", and pin it
// for subsequent calls without further failed round-trips.
func TestCall_LearnsReasoningEffortNoneWithTools(t *testing.T) {
	tools := []ToolDef{{
		Type: "function",
		Function: FunctionDef{
			Name:        "echo",
			Description: "echoes input",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	var mu sync.Mutex
	var efforts []string // recorded reasoning_effort per request ("" = absent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		effort, _ := parsed["reasoning_effort"].(string)
		mu.Lock()
		efforts = append(efforts, effort)
		mu.Unlock()
		toolsArr, _ := parsed["tools"].([]any)
		if len(toolsArr) > 0 && effort != "none" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Function tools with reasoning_effort are not supported","type":"invalid_request_error","param":"reasoning_effort"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "gpt-5.6-luna", "high", 0, 0)
	msgs := []Message{{Role: "user", Content: "hi"}}

	// First call: 400 (effort "high") → learned retry with "none" → success.
	if _, err := c.Call(context.Background(), msgs, nil, tools); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	// Second call: must send "none" immediately, no failed attempt first.
	if _, err := c.Call(context.Background(), msgs, nil, tools); err != nil {
		t.Fatalf("second Call: %v", err)
	}
	// Calls without tools keep the configured effort.
	if _, err := c.Call(context.Background(), msgs, nil, nil); err != nil {
		t.Fatalf("tool-less Call: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"high", "none", "none", "high"}
	if len(efforts) != len(want) {
		t.Fatalf("requests = %v, want %v", efforts, want)
	}
	for i := range want {
		if efforts[i] != want[i] {
			t.Fatalf("requests = %v, want %v", efforts, want)
		}
	}
}

// The thinking configuration must be mapped onto the shape each provider
// accepts: the Anthropic-style "thinking" object for Anthropic/DeepSeek,
// reasoning_effort for OpenAI reasoning models, and nothing otherwise —
// OpenAI rejects unknown top-level parameters with a 400.
func TestBuildCallParams_ThinkingMapping(t *testing.T) {
	cases := []struct {
		name         string
		baseURL      string
		model        string
		thinking     string
		wantThinking string // "" = no thinking object
		wantEffort   string // "" = no reasoning_effort
	}{
		{"deepseek enabled", "https://api.deepseek.com/v1", "deepseek-chat", "enabled", "enabled", ""},
		{"deepseek disabled", "https://api.deepseek.com/v1", "deepseek-chat", "disabled", "disabled", ""},
		{"anthropic enabled", "https://api.anthropic.com/v1", "claude-sonnet-4-5", "enabled", "enabled", ""},
		{"anthropic disabled", "https://api.anthropic.com/v1", "claude-sonnet-4-5", "disabled", "disabled", ""},
		{"openai reasoning disabled", "https://api.openai.com/v1", "gpt-5-nano", "disabled", "", "none"},
		{"openai reasoning enabled", "https://api.openai.com/v1", "gpt-5.6-luna", "enabled", "", "high"},
		{"openai reasoning effort passthrough", "https://api.openai.com/v1", "gpt-5-nano", "medium", "", "medium"},
		{"openai non-reasoning disabled", "https://api.openai.com/v1", "gpt-4o-mini", "disabled", "", ""},
		{"openai non-reasoning enabled", "https://api.openai.com/v1", "gpt-4o-mini", "enabled", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.baseURL, "sk-test", tc.model, tc.thinking, 0, 0)
			body := c.buildCallParams([]Message{{Role: "user", Content: "hi"}}, nil, nil)

			gotThinking := ""
			if body.Thinking != nil {
				gotThinking = body.Thinking.Type
			}
			if gotThinking != tc.wantThinking {
				t.Errorf("thinking object = %q, want %q", gotThinking, tc.wantThinking)
			}
			if body.ReasoningEffort != tc.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", body.ReasoningEffort, tc.wantEffort)
			}
		})
	}
}
