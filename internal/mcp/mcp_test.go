package mcp

import (
	"context"
	"encoding/json"
	"errors"
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

type contextNamedTool struct {
	namedTool
	ctx context.Context
}

func (t *contextNamedTool) SetContext(ctx context.Context) { t.ctx = ctx }
func (t *contextNamedTool) Call(string) (string, error) {
	value, _ := t.ctx.Value(contextTestKey{}).(string)
	return value, nil
}

type contextTestKey struct{}

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

func TestBuildNativeTools_PropagatesRequestContext(t *testing.T) {
	tools := BuildNativeTools([]ToolCaller{
		&contextNamedTool{namedTool: namedTool{name: "context"}},
	})
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"context","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"
	var output strings.Builder
	server := NewServer("v0.0.0-test", tools, strings.NewReader(input), &output)
	ctx := context.WithValue(context.Background(), contextTestKey{}, "request-context")
	if err := server.Run(ctx); err != nil {
		t.Fatalf("server error: %v", err)
	}
	if !strings.Contains(output.String(), "request-context") {
		t.Fatalf("request context did not reach tool: %s", output.String())
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
	if resp.Result.ServerInfo.Name != "odek" {
		t.Errorf("server name = %q, want %q", resp.Result.ServerInfo.Name, "odek")
	}
}

func TestServer_InitializeDefaultsToModernProtocol(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if resp.Result.ProtocolVersion != "2026-07-28" {
		t.Errorf("protocol version = %q, want 2026-07-28", resp.Result.ProtocolVersion)
	}
}

func TestRED_Server_DiscoverAdvertisesModernProtocol(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"server/discover"}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var resp struct {
		Result struct {
			ResultType        string   `json:"resultType"`
			SupportedVersions []string `json:"supportedVersions"`
			TTLMs             int64    `json:"ttlMs"`
			CacheScope        string   `json:"cacheScope"`
			Meta              struct {
				ServerInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"io.modelcontextprotocol/serverInfo"`
			} `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if resp.Result.ResultType != "complete" {
		t.Errorf("resultType = %q, want complete", resp.Result.ResultType)
	}
	if !containsString(resp.Result.SupportedVersions, "2026-07-28") {
		t.Errorf("supportedVersions = %v, want 2026-07-28", resp.Result.SupportedVersions)
	}
	if resp.Result.TTLMs <= 0 || resp.Result.CacheScope != "public" {
		t.Errorf("cache metadata = ttlMs:%d scope:%q", resp.Result.TTLMs, resp.Result.CacheScope)
	}
	if resp.Result.Meta.ServerInfo.Name != "odek" ||
		resp.Result.Meta.ServerInfo.Version != "v0.0.0-test" {
		t.Errorf("server metadata = %+v", resp.Result.Meta.ServerInfo)
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

func TestRED_Server_ModernToolsListMetadataAndOrder(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", []NativeTool{
		{Name: "zeta", Schema: map[string]any{"type": "object"}},
		{Name: "alpha", Schema: map[string]any{"type": "object"}},
	}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			ResultType string         `json:"resultType"`
			TTLMs      *int64         `json:"ttlMs"`
			CacheScope string         `json:"cacheScope"`
			Meta       map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if len(resp.Result.Tools) != 2 ||
		resp.Result.Tools[0].Name != "alpha" ||
		resp.Result.Tools[1].Name != "zeta" {
		t.Fatalf("tools not deterministic: %+v", resp.Result.Tools)
	}
	if resp.Result.ResultType != "complete" || resp.Result.TTLMs == nil ||
		resp.Result.CacheScope != "public" || resp.Result.Meta == nil {
		t.Errorf("missing modern metadata: %+v", resp.Result)
	}
}

func TestServer_LegacyToolsListOmitsModernMetadata(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, lines[1])
	}
	for _, field := range []string{"resultType", "ttlMs", "cacheScope", "_meta"} {
		if _, ok := resp.Result[field]; ok {
			t.Errorf("legacy response unexpectedly contains %q: %s", field, lines[1])
		}
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

func TestServer_StatelessToolCallRequiresModernMetadata(t *testing.T) {
	invoked := false
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"side_effect","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"side_effect","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"side_effect","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":null}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{{
		Name:   "side_effect",
		Schema: map[string]any{"type": "object"},
		CallFn: func(string) (string, error) {
			invoked = true
			return "unsafe", nil
		},
	}}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	if invoked {
		t.Fatal("metadata-free stateless request invoked tool")
	}
	if !strings.Contains(result, `"isError":true`) ||
		!strings.Contains(result, "protocol negotiation required") {
		t.Fatalf("malformed stateless call was not rejected: %s", result)
	}
	if strings.Count(result, `"isError":true`) != 3 {
		t.Fatalf("all incomplete negotiation forms must be rejected: %s", result)
	}
}

func TestServer_RejectedInitializeDoesNotAuthorizeLegacyToolCall(t *testing.T) {
	invoked := false
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"side_effect","arguments":{}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{{
		Name:   "side_effect",
		Schema: map[string]any{"type": "object"},
		CallFn: func(string) (string, error) {
			invoked = true
			return "unsafe", nil
		},
	}}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	if invoked {
		t.Fatal("rejected initialize authorized a metadata-free tool call")
	}
	if !strings.Contains(result, `"code":-32022`) ||
		!strings.Contains(result, "protocol negotiation required") {
		t.Fatalf("unexpected responses: %s", result)
	}
}

func TestServer_InitializeNotificationDoesNotAuthorizeLegacyToolCall(t *testing.T) {
	invoked := false
	input := joinLines(
		`{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":null,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"side_effect","arguments":{}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{{
		Name:   "side_effect",
		Schema: map[string]any{"type": "object"},
		CallFn: func(string) (string, error) {
			invoked = true
			return "unsafe", nil
		},
	}}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	if invoked {
		t.Fatal("initialize notification authorized a metadata-free tool call")
	}
	if !strings.Contains(result, "protocol negotiation required") {
		t.Fatalf("metadata-free call was not rejected: %s", result)
	}
}

func TestServer_LegacyInitializeDoesNotAuthorizeIncompleteModernCall(t *testing.T) {
	invoked := false
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"side_effect","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{{
		Name:   "side_effect",
		Schema: map[string]any{"type": "object"},
		CallFn: func(string) (string, error) {
			invoked = true
			return "unsafe", nil
		},
	}}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	if invoked {
		t.Fatal("legacy state authorized an incomplete modern tool call")
	}
	if !strings.Contains(result, "protocol negotiation required") {
		t.Fatalf("incomplete modern call was not rejected: %s", result)
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

func TestRED_Server_ToolsListBeforeInitIsStateless(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}

	var resp struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if resp.Result.Tools == nil {
		t.Errorf("stateless tools/list did not return a tools array: %s", result)
	}
}

func TestRED_Server_RejectsUnsupportedRequestProtocol(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}` + "\n"
	result, err := serverWithInput("v0.0.0-test", nil, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Requested string   `json:"requested"`
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, result)
	}
	if resp.Error.Code != -32022 || resp.Error.Data.Requested != "1999-01-01" ||
		!containsString(resp.Error.Data.Supported, "2026-07-28") {
		t.Errorf("unsupported-version response = %+v", resp.Error)
	}
}

func TestRED_Server_HandlerPanicIsContained(t *testing.T) {
	input := joinLines(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
	)
	result, err := serverWithInput("v0.0.0-test", []NativeTool{{
		Name:   "boom",
		Schema: map[string]any{"type": "object"},
		CallFn: func(string) (string, error) {
			panic("sensitive panic detail")
		},
	}}, input)
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 2 {
		t.Fatalf("server did not continue after panic: %s", result)
	}
	if !strings.Contains(lines[0], `"isError":true`) ||
		strings.Contains(lines[0], "sensitive panic detail") {
		t.Errorf("unsafe panic response: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"id":2`) {
		t.Errorf("missing response after panic: %s", lines[1])
	}
}

func TestRED_Server_RequestLimitRejectsAndContinues(t *testing.T) {
	var buf strings.Builder
	s := NewServer("v0.0.0-test", nil, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"`+strings.Repeat("x", 256)+`"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`+"\n",
	), &buf)
	s.gmcp.MaxRequestBytes = 128
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("server error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"code":-32600`) ||
		!strings.Contains(lines[1], `"id":2`) {
		t.Errorf("request-limit recovery failed: %s", buf.String())
	}
}

func TestRED_Server_RunPropagatesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewServer("v0.0.0-test", nil, strings.NewReader(""), &strings.Builder{})
	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func serverWithInput(version string, tools []NativeTool, input string) (string, error) {
	r := strings.NewReader(input)
	var buf strings.Builder

	s := NewServer(version, tools, r, &buf)
	err := s.Run(nil)

	return buf.String(), err
}
