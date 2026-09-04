package extended

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llmclient"
)

type dummyLLM struct{}

func (d *dummyLLM) SimpleCall(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func testMainClient(t *testing.T) *llmclient.Client {
	t.Helper()
	c, err := llmclient.Dial("", "main-model", "main-key", "https://api.example.com/v1")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	c.Thinking = "enabled"
	c.MaxTokens = 4096
	c.Temperature = 0.7
	return c
}

func TestResolveLLMFallbackToMain(t *testing.T) {
	main := &dummyLLM{}
	llm := ResolveLLM(Config{}, main, "enabled")
	if llm != main {
		t.Error("expected ResolveLLM to return main LLM when cfg.LLM is nil")
	}
}

func TestResolveLLMThinkingWarning(t *testing.T) {
	main := &dummyLLM{}
	llm := ResolveLLM(Config{}, main, "enabled")
	if llm != main {
		t.Error("expected main LLM fallback")
	}
}

func TestResolveLLMDedicatedRequiresMainClient(t *testing.T) {
	main := &dummyLLM{}
	cfg := Config{
		LLM: &LLMConfig{
			BaseURL: "https://api.example.com/v1",
			APIKey:  "test-key",
			Model:   "test-model",
		},
	}
	llm := ResolveLLM(cfg, main, "")
	if llm != main {
		t.Error("expected fallback when main is not *llmclient.Client")
	}
}

func TestResolveLLMDedicatedFromMainSDK(t *testing.T) {
	main := testMainClient(t)
	cfg := Config{
		LLM: &LLMConfig{Model: "test-model"},
	}
	got := ResolveLLM(cfg, main, "")
	if got == main {
		t.Error("expected dedicated LLM client, got main")
	}
	client, ok := got.(*llmclient.Client)
	if !ok {
		t.Fatalf("expected *llmclient.Client, got %T", got)
	}
	if client.Model() != "test-model" {
		t.Errorf("Model = %q, want test-model", client.Model())
	}
	if client.SDK != main.SDK {
		t.Error("dedicated client must share the main SDK")
	}
}

func TestResolveLLMIncompleteDedicatedFallsBack(t *testing.T) {
	main := &dummyLLM{}
	cfg := Config{
		LLM: &LLMConfig{Model: "test-model"},
	}
	llm := ResolveLLM(cfg, main, "")
	if llm != main {
		t.Error("expected fallback to main LLM when dedicated config is incomplete")
	}
}

func TestResolveLLMWithTimeout(t *testing.T) {
	main := testMainClient(t)
	cfg := Config{
		LLM: &LLMConfig{
			Model:          "test-model",
			TimeoutSeconds: 5,
		},
	}
	got := ResolveLLM(cfg, main, "")
	client, ok := got.(*llmclient.Client)
	if !ok || client == main {
		t.Fatal("expected dedicated LLM client")
	}
	if client.RequestTimeout() != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", client.RequestTimeout())
	}
}

func TestResolveLLMInheritsMainSettings(t *testing.T) {
	main := testMainClient(t)
	cfg := Config{
		LLM: &LLMConfig{Thinking: "disabled"},
	}
	got := ResolveLLM(cfg, main, "enabled")
	client, ok := got.(*llmclient.Client)
	if !ok {
		t.Fatalf("expected *llmclient.Client, got %T", got)
	}
	if client == main {
		t.Fatal("expected a dedicated client, got the main client")
	}
	if client.Model() != main.Model() {
		t.Errorf("Model = %q, want inherited %q", client.Model(), main.Model())
	}
	if client.Thinking != "disabled" {
		t.Errorf("Thinking = %q, want %q", client.Thinking, "disabled")
	}
	if client.MaxTokens != main.MaxTokens {
		t.Errorf("MaxTokens = %d, want inherited %d", client.MaxTokens, main.MaxTokens)
	}
	if client.Temperature != main.Temperature {
		t.Errorf("Temperature = %v, want inherited %v", client.Temperature, main.Temperature)
	}
}

func TestResolveLLMPartialOverrideKeepsInheritedRest(t *testing.T) {
	main := testMainClient(t)
	cfg := Config{
		LLM: &LLMConfig{Model: "cheap-model"},
	}
	got := ResolveLLM(cfg, main, "enabled")
	client, ok := got.(*llmclient.Client)
	if !ok {
		t.Fatalf("expected *llmclient.Client, got %T", got)
	}
	if client.Model() != "cheap-model" {
		t.Errorf("Model = %q, want %q", client.Model(), "cheap-model")
	}
	if client.Thinking != main.Thinking {
		t.Errorf("Thinking = %q, want inherited %q", client.Thinking, main.Thinking)
	}
	if client.SDK != main.SDK {
		t.Error("must share SDK with main")
	}
}
