package main

// Tests for the config_view / list_tools introspection tools and the shared
// view builders they use (introspect.go). The builders are shared with the
// REST management API handlers (handleConfigView, handleLimits,
// handleMCPServers, handleTools) so the tool face and the REST face of the
// same sanitized state can never drift apart — pinned by the parity tests.

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

// introspectionFixture returns a resolved config carrying the fields the
// introspection surfaces must render — plus secret fields that must never
// appear in any rendered view.
func introspectionFixture() config.ResolvedConfig {
	return config.ResolvedConfig{
		Model:          "test-model",
		Stream:         true,
		MaxIter:        42,
		MaxConcurrency: 3,
		// Secrets: structural sanitization must exclude these from the
		// view at build time — no render-time filtering to forget.
		BaseURL: "https://llm.internal.example/v1",
		APIKey:  "sk-fixture-secret-123",
		MCPServers: map[string]mcpclient.ServerConfig{
			"fs": {
				Command:          "npx",
				Args:             []string{"-y", "@model/filesystem", "--api-key", "sk-argv-secret-789"},
				Env:              map[string]string{"FS_TOKEN": "env-secret-456"},
				AutoApprove:      true,
				TimeoutSeconds:   60,
				MaxResponseBytes: 1 << 20,
				MaxResultChars:   1000,
			},
		},
		Subagent: config.SubagentResolved{
			MaxConcurrency: 2,
			TimeoutSeconds: 1800,
			MaxIterations:  100,
			MaxDepth:       3,
			AnnounceBudget: true,
			BudgetInherit:  "operator",
			DefaultProfile: "default",
		},
		Background: config.DefaultBackgroundConfig(),
		Limits: budget.Limits{
			MaxToolCalls: 50,
			ModelPrices: map[string]budget.ModelPrice{
				"test-model": {InputCostPerMillionUSD: 1.5, OutputCostPerMillionUSD: 6},
			},
		},
	}
}

// ── config_view ──────────────────────────────────────────────────────────

func TestConfigViewToolSections(t *testing.T) {
	view := buildConfigView(introspectionFixture())
	tool := &configViewTool{view: view}

	t.Run("empty section defaults to all", func(t *testing.T) {
		out, err := tool.Call("")
		if err != nil {
			t.Fatalf("Call(\"\") error: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, key := range []string{"provider", "model", "sandbox", "memory", "skills", "tools",
			"maintenance", "dangerous_default_action", "guard_scan",
			"subagent", "background", "limits"} {
			if _, ok := m[key]; !ok {
				t.Errorf("all-section output missing key %q", key)
			}
		}
	})

	t.Run("security section", func(t *testing.T) {
		out, err := tool.Call("security")
		if err != nil {
			t.Fatalf("Call(security) error: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, key := range []string{"sandbox", "dangerous_default_action", "guard_scan", "tools"} {
			if _, ok := m[key]; !ok {
				t.Errorf("security section missing %q", key)
			}
		}
		for _, key := range []string{"model", "memory", "limits", "background"} {
			if _, ok := m[key]; ok {
				t.Errorf("security section must not carry %q", key)
			}
		}
	})

	t.Run("subagent section", func(t *testing.T) {
		out, err := tool.Call("subagent")
		if err != nil {
			t.Fatalf("Call(subagent) error: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		sub, ok := m["subagent"].(map[string]any)
		if !ok {
			t.Fatalf("subagent section missing subagent object: %s", out)
		}
		if sub["timeout_seconds"].(float64) != 1800 {
			t.Errorf("subagent.timeout_seconds = %v, want 1800", sub["timeout_seconds"])
		}
		if sub["max_iterations"].(float64) != 100 {
			t.Errorf("subagent.max_iterations = %v, want 100", sub["max_iterations"])
		}
		if sub["default_profile"] != "default" {
			t.Errorf("subagent.default_profile = %v, want default", sub["default_profile"])
		}
		if sub["announce_budget"] != true {
			t.Errorf("subagent.announce_budget = %v, want true", sub["announce_budget"])
		}
	})

	t.Run("background section", func(t *testing.T) {
		out, err := tool.Call("background")
		if err != nil {
			t.Fatalf("Call(background) error: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		bg, ok := m["background"].(map[string]any)
		if !ok {
			t.Fatalf("background section missing background object: %s", out)
		}
		if bg["enabled"] != true || bg["wake_on_complete"] != true {
			t.Errorf("background defaults not rendered: %s", out)
		}
	})

	t.Run("unknown section errors with valid names", func(t *testing.T) {
		_, err := tool.Call("bogus")
		if err == nil {
			t.Fatal("unknown section must error")
		}
		if !strings.Contains(err.Error(), "security") {
			t.Errorf("error should list valid sections, got: %v", err)
		}
	})
}

func TestConfigViewToolDoesNotLeakSecrets(t *testing.T) {
	view := buildConfigView(introspectionFixture())
	tool := &configViewTool{view: view}

	out, err := tool.Call("")
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	for _, secret := range []string{
		"sk-fixture-secret-123",
		"llm.internal.example",
		"env-secret-456",
		"FS_TOKEN",
		"api_key",
		"base_url",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("sanitized config view leaks %q", secret)
		}
	}
}

// ── list_tools ───────────────────────────────────────────────────────────

func TestListToolsToolReportsFilterState(t *testing.T) {
	resolved := introspectionFixture()
	tool := &listToolsTool{
		registered:    []string{"shell", "read_file", "math_eval"},
		toolsEnabled:  []string{"read_file"}, // whitelist active
		toolsDisabled: []string{"shell"},
		mcpServers:    buildMCPServersView(resolved),
	}
	out, err := tool.Call("")
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var m struct {
		Tools []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
		MCPServers []map[string]any `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	enabled := map[string]bool{}
	for _, tl := range m.Tools {
		enabled[tl.Name] = tl.Enabled
	}
	if len(m.Tools) != 3 {
		t.Fatalf("tools = %d entries, want 3", len(m.Tools))
	}
	// Whitelist active + shell disabled ⇒ shell off; read_file on; math_eval
	// not whitelisted ⇒ off.
	if enabled["shell"] {
		t.Error("shell should be disabled (disabled list)")
	}
	if !enabled["read_file"] {
		t.Error("read_file should be enabled (whitelisted)")
	}
	if enabled["math_eval"] {
		t.Error("math_eval should be disabled (not on active whitelist)")
	}
	if len(m.MCPServers) != 1 || m.MCPServers[0]["name"] != "fs" {
		t.Fatalf("mcp_servers not rendered: %s", out)
	}
	if _, ok := m.MCPServers[0]["env"]; ok {
		t.Error("mcp server entry must not carry env values")
	}
}

// ── zero-drift parity: REST face == tool face ────────────────────────────

func TestRESTConfigViewMatchesToolAll(t *testing.T) {
	resolved := introspectionFixture()

	w := httptest.NewRecorder()
	handleConfigView(resolved)(w, httptest.NewRequest("GET", "/api/config", nil))
	var fromREST map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fromREST); err != nil {
		t.Fatalf("decode REST body: %v", err)
	}

	tool := &configViewTool{view: buildConfigView(resolved)}
	out, err := tool.Call("")
	if err != nil {
		t.Fatalf("tool Call: %v", err)
	}
	var fromTool map[string]any
	if err := json.Unmarshal([]byte(out), &fromTool); err != nil {
		t.Fatalf("decode tool body: %v", err)
	}

	if !reflect.DeepEqual(fromREST, fromTool) {
		t.Error("GET /api/config and config_view(all) diverge — shared builder violated")
	}
}

func TestRESTLimitsMatchesSharedBuilder(t *testing.T) {
	limits := introspectionFixture().Limits

	w := httptest.NewRecorder()
	handleLimits("test-model", limits)(w, httptest.NewRequest("GET", "/api/limits", nil))
	var fromREST map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fromREST); err != nil {
		t.Fatalf("decode REST body: %v", err)
	}

	builderView := buildLimitsView("test-model", limits)
	// Compare like-for-like: decode the builder output through JSON so the
	// struct-typed Limits is seen exactly as both faces marshal it.
	builderJSON, err := json.Marshal(builderView)
	if err != nil {
		t.Fatalf("marshal builder view: %v", err)
	}
	var fromBuilder map[string]any
	if err := json.Unmarshal(builderJSON, &fromBuilder); err != nil {
		t.Fatalf("decode builder view: %v", err)
	}
	if !reflect.DeepEqual(fromREST, fromBuilder) {
		t.Errorf("GET /api/limits and buildLimitsView diverge:\nrest:  %v\ntool:  %v", fromREST, fromBuilder)
	}
}

// ── registration wiring ──────────────────────────────────────────────────

func TestIntrospectionToolsAreReserved(t *testing.T) {
	reserved := reservedBuiltinToolNames()
	for _, name := range []string{"config_view", "list_tools"} {
		if !reserved[name] {
			t.Errorf("reservedBuiltinToolNames missing %q — an MCP server could shadow it", name)
		}
	}
}

func TestToolConfigFromResolvedBuildsIntrospection(t *testing.T) {
	tc := toolConfigFromResolved(introspectionFixture())
	if tc.Introspection.ConfigView == nil {
		t.Fatal("toolConfigFromResolved did not build Introspection.ConfigView")
	}
	if _, ok := tc.Introspection.ConfigView["subagent"]; !ok {
		t.Error("ConfigView missing subagent section")
	}
	func() {
		// Tool face must redact credential-ish argv (goes through the REAL
		// wiring path: toolConfigFromResolved → buildIntrospectionState).
		tc := toolConfigFromResolved(introspectionFixture())
		mcpJSON, err := json.Marshal(tc.Introspection.MCPServers)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(mcpJSON), "sk-argv-secret-789") {
			t.Error("Introspection.MCPServers leaks credential argv — redaction not wired into the tool face")
		}
		if !strings.Contains(string(mcpJSON), "@model/filesystem") {
			t.Error("tool face over-redacted benign argv")
		}
	}()
}

func TestConfigViewToolToleratesNilView(t *testing.T) {
	// reservedBuiltinToolNames constructs builtinTools with a zero
	// toolConfig — the tools must survive a nil/empty view.
	tool := &configViewTool{view: nil}
	if _, err := tool.Call(""); err != nil {
		t.Fatalf("nil view must not error: %v", err)
	}
}

// ── wiring: builtinTools must populate the live registry ────────────────

func TestBuiltinToolsWiresListToolsRegistry(t *testing.T) {
	// P1 regression: the live-name capture was silently missing, so
	// production list_tools always reported an empty registry while the
	// hand-built test struct stayed green.
	tc := toolConfigFromResolved(introspectionFixture())
	tools := builtinTools(danger.DangerousConfig{}, nil, nil, 1, "", tc, nil)

	var listTools *listToolsTool
	seen := map[string]bool{}
	for _, tl := range tools {
		seen[tl.Name()] = true
		if lt, ok := tl.(*listToolsTool); ok {
			listTools = lt
		}
	}
	if listTools == nil {
		t.Fatal("list_tools not registered by builtinTools")
	}
	for _, want := range []string{"config_view", "list_tools", "read_file", "shell", "list_subagent_profiles"} {
		if !seen[want] {
			t.Errorf("builtinTools registry missing %q", want)
		}
		if !listTools.registeredWant(want) {
			t.Errorf("list_tools.registered does not report %q — live capture missing", want)
		}
	}
}

// registeredWant reports whether name is in the captured registry.
func (t *listToolsTool) registeredWant(name string) bool {
	for _, n := range t.registered {
		if n == name {
			return true
		}
	}
	return false
}

// ── MCP argv redaction on the tool face ─────────────────────────────────

func TestMCPServerArgsRedactedOnToolFace(t *testing.T) {
	// P2: MCP argv can carry credentials (--api-key sk-…). The REST face
	// keeps verbatim argv (CSRF-gated, operator-only); the tool face must
	// redact credential-ish values before they enter model context.
	resolved := introspectionFixture()
	resolved.MCPServers["secretary"] = mcpclient.ServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-harbor", "--api-key", "sk-argv-secret-789", "--verbose", "--token=BearerXYZ", "--port", "8080"},
		Env:     map[string]string{"HARBOR_TOKEN": "env-secret-456"},
	}

	restFace := buildMCPServersView(resolved)
	toolFace := redactMCPServersView(buildMCPServersView(resolved))

	restJSON, _ := json.Marshal(restFace)
	toolJSON, _ := json.Marshal(toolFace)
	for _, secret := range []string{"sk-argv-secret-789", "BearerXYZ"} {
		if !strings.Contains(string(restJSON), secret) {
			t.Errorf("REST face must keep operator argv verbatim; lost %q", secret)
		}
		if strings.Contains(string(toolJSON), secret) {
			t.Errorf("tool face leaks argv credential %q", secret)
		}
	}
	var entries []mcpEntry
	if err := json.Unmarshal(toolJSON, &entries); err != nil {
		t.Fatalf("decode tool face: %v", err)
	}
	// Non-credential args and commands survive unredacted.
	joined := toolJSON
	for _, want := range []string{"mcp-harbor", "--verbose", "--port", "8080", "uvx"} {
		if !strings.Contains(string(joined), want) {
			t.Errorf("tool face over-redacted benign content: lost %q", want)
		}
	}
}
