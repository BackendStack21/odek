package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	c := New("https://api.example.com/v1", "sk-key", "gpt-4", "enabled", 0)
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
	c := New("https://api.example.com/v1/", "sk-key", "model", "", 0)
	if c.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL should trim trailing slash, got %q", c.BaseURL)
	}
}

func TestClient_New_CustomTimeout(t *testing.T) {
	c := New("https://api.example.com", "sk-key", "model", "", 30*time.Second)
	if c.http.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestClient_New_ZeroTimeoutUsesDefault(t *testing.T) {
	c := New("https://api.example.com", "sk-key", "model", "", 0)
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

	c := New(server.URL, "sk-test", "test-model", "", 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}
}

func TestClient_Call_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClient_Call_WithThinking(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"thinking response"}}]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "deepseek-chat", "enabled", 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "think"}}, nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.Content != "thinking response" {
		t.Errorf("Content = %q", result.Content)
	}
	// Verify thinking was sent in the request
	thinking, ok := receivedBody["thinking"]
	if !ok {
		t.Error("thinking field not sent in request")
	}
	thinkingMap, ok := thinking.(map[string]any)
	if !ok || thinkingMap["type"] != "enabled" {
		t.Errorf("thinking = %v, want {type: enabled}", thinking)
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

	c := New(server.URL, "sk-test", "o1", "high", 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "reason"}}, nil)
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
	c := New("http://127.0.0.1:1", "sk-test", "model", "", 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
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

	c := New(server.URL, "sk-test", "test-model", "", 0)
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
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools)
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

	c := New(server.URL, "sk-bad", "test-model", "", 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

// Test Call() with invalid JSON in the response body.
func TestClient_Call_InvalidJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0)
	_, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
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

	c := New(server.URL, "sk-test", "test-model", "", 0)
	result, err := c.Call(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
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

	c := New(server.URL, "sk-test", "test-model", "", 0)
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

	c := New(server.URL, "sk-test", "test-model", "", 0)
	_, err := c.SimpleCall(context.Background(), "bot", "hi")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestClient_SimpleCall_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	c := New(server.URL, "sk-test", "test-model", "", 0)
	_, err := c.SimpleCall(context.Background(), "bot", "hi")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}
