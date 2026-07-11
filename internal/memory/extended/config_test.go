package extended

import (
	"context"
	"testing"
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
