package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

type namedTool struct{ name string }

func (n *namedTool) Name() string        { return n.name }
func (n *namedTool) Description() string { return "tool " + n.name }
func (n *namedTool) Schema() any         { return map[string]any{"type": "object"} }
func (n *namedTool) Call(args string) (string, error) {
	return "ok", nil
}

func TestBuildNativeTools_SkipsDelegateTasks(t *testing.T) {
	tools := BuildNativeTools([]ToolCaller{
		&namedTool{name: "delegate_tasks"},
		&namedTool{name: "memory"},
		&namedTool{name: "shell"},
	})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (shell), got %d", len(tools))
	}
	if tools[0].Name != "shell" {
		t.Errorf("expected shell, got %s", tools[0].Name)
	}
}

func TestServer_Initialize(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if resp.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("protocol version = %q, want %q", resp.Result.ProtocolVersion, "2025-03-26")
	}
	if resp.Result.ServerInfo.Name != "kode" {
		t.Errorf("server name = %q, want %q", resp.Result.ServerInfo.Name, "kode")
	}
}

func TestServer_ToolsList(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{
		{Name: "shell", Description: "Run a command", Schema: map[string]any{"type": "object"}},
	}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected >=2 response lines, got %d\n%s", len(lines), result)
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("unmarshal tools/list: %v\nraw: %s", err, lines[1])
	}
	if len(listResp.Result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(listResp.Result.Tools))
	}
	if listResp.Result.Tools[0].Name != "shell" {
		t.Errorf("tool name = %q, want %q", listResp.Result.Tools[0].Name, "shell")
	}
}

func TestServer_ToolCall(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"input":"hello"}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{
		{
			Name:        "echo",
			Description: "Echo input",
			Schema:      map[string]any{"type": "object"},
			CallFn: func(args string) (string, error) {
				return "echo: " + args, nil
			},
		},
	}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	var callResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &callResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, lines[1])
	}
	if len(callResp.Result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(callResp.Result.Content))
	}
	if !strings.Contains(callResp.Result.Content[0].Text, "hello") {
		t.Errorf("content = %q, want substring 'hello'", callResp.Result.Content[0].Text)
	}
}

func TestServer_UnknownTool(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	var callResp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[1]), &callResp)
	if !callResp.Result.IsError {
		t.Error("expected isError=true for unknown tool")
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"unknown_method","params":{}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var errResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if errResp.Error.Code != -32601 {
		t.Errorf("error code = %d, want %d", errResp.Error.Code, -32601)
	}
}

func TestServer_ToolsListBeforeInit(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(result), &errResp)
	if !strings.Contains(errResp.Error.Message, "Not initialized") {
		t.Errorf("expected 'Not initialized', got %q", errResp.Error.Message)
	}
}

func TestServer_Ping(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
	)
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	var pongResp struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal([]byte(lines[1]), &pongResp)
	if pongResp.Result == nil {
		t.Error("expected non-nil result for ping")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func joinLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func serverWithInput(version string, tools []NativeTool, input string) (string, error) {
	r := strings.NewReader(input)
	var buf strings.Builder

	s := NewServer(version, tools, r, &buf)
	err := s.Run(nil)

	return buf.String(), err
}
