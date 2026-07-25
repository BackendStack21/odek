package extended

import (
	"context"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
)

type dummyLLM struct{}

func (d *dummyLLM) SimpleCall(_ context.Context, _, _ string) (string, error) {
	return "", nil
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
	// We can't easily capture stderr here, but we can exercise the path.
	llm := ResolveLLM(Config{}, main, "enabled")
	if llm != main {
		t.Error("expected main LLM fallback")
	}
}

func TestResolveLLMDedicated(t *testing.T) {
	main := &dummyLLM{}
	cfg := Config{
		LLM: &LLMConfig{
			BaseURL: "https://api.example.com/v1",
			APIKey:  "test-key",
			Model:   "test-model",
		},
	}
	llm := ResolveLLM(cfg, main, "")
	if llm == main {
		t.Error("expected dedicated LLM client, got main")
	}
}

func TestResolveLLMIncompleteDedicatedFallsBack(t *testing.T) {
	main := &dummyLLM{}
	cfg := Config{
		LLM: &LLMConfig{Model: "test-model"}, // missing BaseURL
	}
	llm := ResolveLLM(cfg, main, "")
	if llm != main {
		t.Error("expected fallback to main LLM when dedicated config is incomplete")
	}
}

func TestResolveLLMWithTimeout(t *testing.T) {
	main := &dummyLLM{}
	cfg := Config{
		LLM: &LLMConfig{
			BaseURL:        "https://api.example.com/v1",
			APIKey:         "test-key",
			Model:          "test-model",
			TimeoutSeconds: 5,
		},
	}
	llm := ResolveLLM(cfg, main, "")
	if llm == main {
		t.Error("expected dedicated LLM client")
	}
}

func TestResolveLLMInheritsMainSettings(t *testing.T) {
	main := llm.NewWithMaxTokens("https://api.example.com/v1/", "main-key", "main-model", "enabled", 0, 4096, 10*time.Second)
	main.Temperature = 0.7
	cfg := Config{
		LLM: &LLMConfig{Thinking: "disabled"}, // override thinking only
	}
	got := ResolveLLM(cfg, main, "enabled")
	client, ok := got.(*llm.Client)
	if !ok {
		t.Fatalf("expected *llm.Client, got %T", got)
	}
	if client == main {
		t.Fatal("expected a dedicated client, got the main client")
	}
	if client.BaseURL != main.BaseURL {
		t.Errorf("BaseURL = %q, want inherited %q", client.BaseURL, main.BaseURL)
	}
	if client.APIKey != main.APIKey {
		t.Errorf("APIKey = %q, want inherited %q", client.APIKey, main.APIKey)
	}
	if client.Model != main.Model {
		t.Errorf("Model = %q, want inherited %q", client.Model, main.Model)
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
	main := llm.New("https://api.example.com/v1", "main-key", "main-model", "enabled", 0, 0)
	cfg := Config{
		LLM: &LLMConfig{Model: "cheap-model"}, // different model, same backend
	}
	got := ResolveLLM(cfg, main, "enabled")
	client, ok := got.(*llm.Client)
	if !ok {
		t.Fatalf("expected *llm.Client, got %T", got)
	}
	if client.Model != "cheap-model" {
		t.Errorf("Model = %q, want %q", client.Model, "cheap-model")
	}
	if client.BaseURL != main.BaseURL || client.APIKey != main.APIKey {
		t.Errorf("expected inherited BaseURL/APIKey, got %q/%q", client.BaseURL, client.APIKey)
	}
	if client.Thinking != main.Thinking {
		t.Errorf("Thinking = %q, want inherited %q", client.Thinking, main.Thinking)
	}
}
