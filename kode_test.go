package kode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/kode/internal/llm"
)

func TestConfigDefaults(t *testing.T) {
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	cfg := Config{
		APIKey: "sk-test",
	}

	if cfg.MaxIterations != 0 {
		t.Error("MaxIterations should default to 0")
	}

	_, err := New(cfg)
	if err != nil {
		t.Fatalf("New() with explicit APIKey should not error: %v", err)
	}
}

func TestConfigDefaultModel(t *testing.T) {
	cfg := Config{APIKey: "sk-test"}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Model != "deepseek-chat" {
		t.Errorf("default model = %q, want %q", agent.config.Model, "deepseek-chat")
	}
}

func TestConfigDefaultBaseURL(t *testing.T) {
	cfg := Config{APIKey: "sk-test"}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("default BaseURL = %q, want %q", agent.config.BaseURL, "https://api.deepseek.com/v1")
	}
}

func TestConfigDefaultMaxIterations(t *testing.T) {
	cfg := Config{APIKey: "sk-test"}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.MaxIterations != 90 {
		t.Errorf("default MaxIterations = %d, want 90", agent.config.MaxIterations)
	}
}

func TestConfigCustomModel(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		Model:  "deepseek-v4-flash",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Model != "deepseek-v4-flash" {
		t.Errorf("model = %q, want %q", agent.config.Model, "deepseek-v4-flash")
	}
}

func TestConfigCustomBaseURL(t *testing.T) {
	cfg := Config{
		APIKey:  "sk-test",
		BaseURL: "https://api.openai.com/v1",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want %q", agent.config.BaseURL, "https://api.openai.com/v1")
	}
}

func TestConfigCustomMaxIterations(t *testing.T) {
	cfg := Config{
		APIKey:        "sk-test",
		MaxIterations: 42,
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.MaxIterations != 42 {
		t.Errorf("MaxIterations = %d, want 42", agent.config.MaxIterations)
	}
}

func TestConfigThinkingPassthrough(t *testing.T) {
	tests := []struct {
		thinking string
	}{
		{"enabled"},
		{"disabled"},
		{"low"},
		{"medium"},
		{"high"},
		{""},
	}

	for _, tt := range tests {
		cfg := Config{
			APIKey:   "sk-test",
			Thinking: tt.thinking,
		}
		agent, err := New(cfg)
		if err != nil {
			t.Fatalf("New() with thinking=%q: %v", tt.thinking, err)
		}
		if agent.config.Thinking != tt.thinking {
			t.Errorf("Thinking = %q, want %q", agent.config.Thinking, tt.thinking)
		}
	}
}

func TestConfigAPIKeyEnvFallback(t *testing.T) {
	t.Run("DEEPSEEK_API_KEY", func(t *testing.T) {
		os.Unsetenv("OPENAI_API_KEY")
		os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")
		defer os.Unsetenv("DEEPSEEK_API_KEY")

		cfg := Config{}
		agent, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if agent.config.APIKey != "sk-deepseek-test" {
			t.Errorf("APIKey = %q, want %q", agent.config.APIKey, "sk-deepseek-test")
		}
	})

	t.Run("OPENAI_API_KEY fallback", func(t *testing.T) {
		os.Unsetenv("DEEPSEEK_API_KEY")
		os.Setenv("OPENAI_API_KEY", "sk-openai-test")
		defer os.Unsetenv("OPENAI_API_KEY")

		cfg := Config{}
		agent, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if agent.config.APIKey != "sk-openai-test" {
			t.Errorf("APIKey = %q, want %q", agent.config.APIKey, "sk-openai-test")
		}
	})

	t.Run("explicit overrides env", func(t *testing.T) {
		os.Setenv("DEEPSEEK_API_KEY", "sk-env")
		defer os.Unsetenv("DEEPSEEK_API_KEY")

		cfg := Config{APIKey: "sk-explicit"}
		agent, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if agent.config.APIKey != "sk-explicit" {
			t.Errorf("APIKey = %q, want %q", agent.config.APIKey, "sk-explicit")
		}
	})
}

func TestConfigNoAPIKey(t *testing.T) {
	os.Unsetenv("DEEPSEEK_API_KEY")
	os.Unsetenv("OPENAI_API_KEY")

	cfg := Config{}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestConfigSystemMessage(t *testing.T) {
	cfg := Config{
		APIKey:        "sk-test",
		SystemMessage: "You are a helpful assistant.",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.SystemMessage != "You are a helpful assistant." {
		t.Errorf("SystemMessage = %q, want %q", agent.config.SystemMessage, "You are a helpful assistant.")
	}
}

func TestAgent_Run(t *testing.T) {
	// Agent.Run delegates to engine.Run. Test that it doesn't panic.
	agent, err := New(Config{
		APIKey: "sk-test",
		Model:  "deepseek-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Run with a cancelled context — should return error quickly
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = agent.Run(ctx, "test task")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAgent_Close_NoSandbox(t *testing.T) {
	agent, err := New(Config{APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	// Close with no sandbox cleanup should return nil
	if err := agent.Close(); err != nil {
		t.Errorf("Close() with no sandbox should return nil, got: %v", err)
	}
}

func TestAgent_Close_WithSandbox(t *testing.T) {
	cleanupCalled := false
	cleanup := func() error {
		cleanupCalled = true
		return nil
	}

	agent, err := New(Config{
		APIKey:         "sk-test",
		SandboxCleanup: cleanup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
	if !cleanupCalled {
		t.Error("sandbox cleanup was not called")
	}
}

func TestAgent_Close_WithSandboxError(t *testing.T) {
	cleanup := func() error {
		return fmt.Errorf("cleanup failed")
	}

	agent, err := New(Config{
		APIKey:         "sk-test",
		SandboxCleanup: cleanup,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.Close()
	if err == nil {
		t.Fatal("expected error from cleanup")
	}
}

func TestToolAdapter(t *testing.T) {
	// Create a fake tool
	fake := &fakeKodeTool{
		name:        "test",
		description: "a test tool",
		schema:      map[string]any{"type": "object"},
		callResult:  "result",
	}

	adapter := &toolAdapter{t: fake}

	if adapter.Name() != "test" {
		t.Errorf("Name() = %q, want %q", adapter.Name(), "test")
	}
	if adapter.Description() != "a test tool" {
		t.Errorf("Description() = %q, want %q", adapter.Description(), "a test tool")
	}
	if adapter.Schema() == nil {
		t.Error("Schema() returned nil")
	}

	result, err := adapter.Call(`{"arg": "value"}`)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result != "result" {
		t.Errorf("Call() = %q, want %q", result, "result")
	}
}

// fakeKodeTool implements kode.Tool for testing.
type fakeKodeTool struct {
	name        string
	description string
	schema      any
	callResult  string
	callError   error
}

func (f *fakeKodeTool) Name() string                     { return f.name }
func (f *fakeKodeTool) Description() string              { return f.description }
func (f *fakeKodeTool) Schema() any                      { return f.schema }
func (f *fakeKodeTool) Call(args string) (string, error) { return f.callResult, f.callError }

// Test that New() works with tools, covering the tool adapter loop (lines 109-112 in kode.go).
func TestNew_WithTools(t *testing.T) {
	fake := &fakeKodeTool{
		name:        "test_tool",
		description: "a test tool",
		schema:      map[string]any{"type": "object"},
		callResult:  "ok",
	}
	cfg := Config{
		APIKey: "sk-test",
		Tools:  []Tool{fake},
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("New() with tools error: %v", err)
	}
	// Verify the tool was registered in the internal registry
	tools := agent.registry.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool in registry, got %d", len(tools))
	}
	if tools[0].Name() != "test_tool" {
		t.Errorf("tool name = %q, want %q", tools[0].Name(), "test_tool")
	}
}

// ── Model Profile Tests ───────────────────────────────────────────────

func TestLookupProfile_ExactMatch(t *testing.T) {
	p := LookupProfile("deepseek-v4-flash")
	if p == nil {
		t.Fatal("LookupProfile(\"deepseek-v4-flash\") returned nil")
	}
	if p.Label != "DeepSeek v4 Flash" {
		t.Errorf("Label = %q, want %q", p.Label, "DeepSeek v4 Flash")
	}
	if p.DefaultThinking != "" {
		t.Errorf("DefaultThinking = %q, want empty", p.DefaultThinking)
	}
	if p.Timeout != 90 {
		t.Errorf("Timeout = %d, want 90", p.Timeout)
	}
}

func TestLookupProfile_ProExactMatch(t *testing.T) {
	p := LookupProfile("deepseek-v4-pro")
	if p == nil {
		t.Fatal("LookupProfile(\"deepseek-v4-pro\") returned nil")
	}
	if p.Label != "DeepSeek v4 Pro" {
		t.Errorf("Label = %q, want %q", p.Label, "DeepSeek v4 Pro")
	}
	if p.DefaultThinking != "enabled" {
		t.Errorf("DefaultThinking = %q, want %q", p.DefaultThinking, "enabled")
	}
	if p.Timeout != 180 {
		t.Errorf("Timeout = %d, want 180", p.Timeout)
	}
}

func TestLookupProfile_LongestPrefixMatch(t *testing.T) {
	// "deepseek-v4-flash-custom" should match "deepseek-v4-flash" not "deepseek-"
	p := LookupProfile("deepseek-v4-flash-custom-v2")
	if p == nil {
		t.Fatal("LookupProfile returned nil")
	}
	if p.Label != "DeepSeek v4 Flash" {
		t.Errorf("Label = %q, want %q", p.Label, "DeepSeek v4 Flash")
	}
}

func TestLookupProfile_FallbackMatch(t *testing.T) {
	// Any other deepseek-* model should match the generic "deepseek-" profile
	p := LookupProfile("deepseek-coder")
	if p == nil {
		t.Fatal("LookupProfile(\"deepseek-coder\") returned nil")
	}
	if p.Label != "DeepSeek (generic)" {
		t.Errorf("Label = %q, want %q", p.Label, "DeepSeek (generic)")
	}
}

func TestLookupProfile_NoMatch(t *testing.T) {
	p := LookupProfile("gpt-4o")
	if p != nil {
		t.Errorf("LookupProfile(\"gpt-4o\") = %v, want nil", p)
	}
}

func TestProfileLabel_Known(t *testing.T) {
	if label := ProfileLabel("deepseek-v4-pro"); label != "DeepSeek v4 Pro" {
		t.Errorf("ProfileLabel = %q, want %q", label, "DeepSeek v4 Pro")
	}
}

func TestProfileLabel_Unknown(t *testing.T) {
	if label := ProfileLabel("gpt-4o"); label != "gpt-4o" {
		t.Errorf("ProfileLabel should return model name for unknown models, got %q", label)
	}
}

func TestNew_ProfileDefaultThinking_Pro(t *testing.T) {
	// deepseek-v4-pro has DefaultThinking="enabled" — applied when empty
	cfg := Config{
		APIKey: "sk-test",
		Model:  "deepseek-v4-pro",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Thinking != "enabled" {
		t.Errorf("Thinking = %q, want %q (profile default)", agent.config.Thinking, "enabled")
	}
}

func TestNew_ProfileDefaultThinking_Flash(t *testing.T) {
	// deepseek-v4-flash has no DefaultThinking — field stays empty
	cfg := Config{
		APIKey: "sk-test",
		Model:  "deepseek-v4-flash",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Thinking != "" {
		t.Errorf("Thinking = %q, want empty (Flash has no thinking default)", agent.config.Thinking)
	}
}

func TestNew_ExplicitThinkingOverridesProfile(t *testing.T) {
	// Explicit Thinking should win over profile default
	cfg := Config{
		APIKey:   "sk-test",
		Model:    "deepseek-v4-pro",
		Thinking: "disabled", // override profile's "enabled"
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Thinking != "disabled" {
		t.Errorf("Thinking = %q, want %q (explicit should override profile)", agent.config.Thinking, "disabled")
	}
}

func TestNew_ProfileTimeout_Pro(t *testing.T) {
	// Verify the profile timeout is passed to the LLM client.
	// We can't directly inspect the client's timeout, but we can verify
	// the agent was created without error.
	cfg := Config{
		APIKey: "sk-test",
		Model:  "deepseek-v4-pro",
	}
	_, err := New(cfg)
	if err != nil {
		t.Fatalf("New() with deepseek-v4-pro should succeed: %v", err)
	}
}

func TestNew_DefaultModelNoProfile(t *testing.T) {
	// deepseek-chat is not in KnownProfiles — no profile defaults applied
	cfg := Config{
		APIKey: "sk-test",
		Model:  "deepseek-chat",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.Thinking != "" {
		t.Errorf("Thinking = %q, want empty for default model", agent.config.Thinking)
	}
}

func TestKnownProfiles_NotEmpty(t *testing.T) {
	if len(KnownProfiles) == 0 {
		t.Error("KnownProfiles should not be empty")
	}
	// Verify all profiles have prefixes
	for _, p := range KnownProfiles {
		if p.Prefix == "" {
			t.Error("Found profile with empty prefix")
		}
	}
}

func TestProfileMaxContext_Pro(t *testing.T) {
	p := LookupProfile("deepseek-v4-pro")
	if p == nil {
		t.Fatal("profile not found")
	}
	if p.MaxContext != 1_000_000 {
		t.Errorf("MaxContext = %d, want 1_000_000", p.MaxContext)
	}
}

func TestProfileMaxContext_Flash(t *testing.T) {
	p := LookupProfile("deepseek-v4-flash")
	if p == nil {
		t.Fatal("profile not found")
	}
	if p.MaxContext != 131_072 {
		t.Errorf("MaxContext = %d, want 131_072", p.MaxContext)
	}
}

func TestProfileMaxContext_Unknown(t *testing.T) {
	p := LookupProfile("gpt-4o")
	if p != nil {
		t.Errorf("LookupProfile for unknown model = %v, want nil", p)
	}
}


// ── Project File (AGENTS.md) Tests ───────────────────────────────────

func TestLoadProjectFile_Missing(t *testing.T) {
	// No AGENTS.md in current dir — should return empty
	content := LoadProjectFile()
	if content != "" {
		t.Errorf("LoadProjectFile() with no file = %q, want empty", content)
	}
}

func TestLoadProjectFile_WithFile(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile("AGENTS.md", []byte("This project uses Go 1.24."), 0644); err != nil {
		t.Fatal(err)
	}

	content := LoadProjectFile()
	if content != "This project uses Go 1.24." {
		t.Errorf("LoadProjectFile() = %q, want %q", content, "This project uses Go 1.24.")
	}
}

func TestLoadProjectFile_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile("AGENTS.md", []byte("  \n  project instructions  \n  "), 0644); err != nil {
		t.Fatal(err)
	}

	content := LoadProjectFile()
	if content != "project instructions" {
		t.Errorf("LoadProjectFile() = %q, want %q", content, "project instructions")
	}
}

func TestNew_ProjectFileAppended(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile("AGENTS.md", []byte("Use tabs, not spaces."), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		APIKey:        "sk-test",
		SystemMessage: "You are a bot.",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agent.config.SystemMessage, "Use tabs, not spaces.") {
		t.Errorf("SystemMessage should contain AGENTS.md content, got: %q", agent.config.SystemMessage)
	}
	if !strings.Contains(agent.config.SystemMessage, "Project Instructions") {
		t.Errorf("SystemMessage should have 'Project Instructions' header, got: %q", agent.config.SystemMessage)
	}
	if !strings.Contains(agent.config.SystemMessage, "You are a bot.") {
		t.Errorf("SystemMessage should keep original content, got: %q", agent.config.SystemMessage)
	}
}

func TestNew_ProjectFileWithNoOriginalSystem(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile("AGENTS.md", []byte("Just these instructions."), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		APIKey: "sk-test",
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.SystemMessage != "# Project Instructions\n\nJust these instructions." {
		t.Errorf("SystemMessage = %q, want 'Project Instructions' + content", agent.config.SystemMessage)
	}
}

func TestNew_NoProjectFileOptOut(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)

	if err := os.WriteFile("AGENTS.md", []byte("Should not appear."), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		APIKey:        "sk-test",
		SystemMessage: "Only this.",
		NoProjectFile: true,
	}
	agent, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if agent.config.SystemMessage != "Only this." {
		t.Errorf("SystemMessage = %q, want original only (no project file)", agent.config.SystemMessage)
	}
}

func TestExpandHome(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	got := expandHome("~/projects/test")
	expected := home + "/projects/test"
	if got != expected {
		t.Errorf("expandHome = %q, want %q", got, expected)
	}
	if got := expandHome("/absolute/path"); got != "/absolute/path" {
		t.Errorf("expandHome(/absolute) = %q", got)
	}
	if got := expandHome("./relative"); got != "./relative" {
		t.Errorf("expandHome(./relative) = %q", got)
	}
}

func TestAgent_Close_NoCleanup(t *testing.T) {
	agent := &Agent{}
	if err := agent.Close(); err != nil {
		t.Errorf("Close with no cleanup: %v", err)
	}
}

func TestAgent_Close_WithCleanup(t *testing.T) {
	called := false
	agent := &Agent{sandboxCleanup: func() error { called = true; return nil }}
	if err := agent.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !called {
		t.Error("cleanup not called")
	}
}

func TestAgent_RunWithMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"agent response"}}]}`))
	}))
	defer server.Close()

	agent, err := New(Config{
		Model:         "test",
		BaseURL:       server.URL,
		APIKey:        "sk-test",
		MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	msgs := []llm.Message{
		{Role: "user", Content: "task"},
	}
	result, _, err := agent.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if result != "agent response" {
		t.Errorf("result = %q", result)
	}
}

func TestLoadProjectFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(cwd)
	content := LoadProjectFile()
	if content != "" {
		t.Errorf("LoadProjectFile in empty dir = %q, want empty", content)
	}
}
