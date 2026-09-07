package llmclient

import (
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// Custom (non built-in) providers registered through NewSDK must receive
// per-format default quirks. Zero quirks silently drop ChatRequest.Thinking
// on OpenAI-format gateways (LiteLLM, OpenRouter, vLLM), so reasoning tokens
// never come back.
func TestNewSDK_DefaultQuirksForCustomProviders(t *testing.T) {
	s, err := NewSDK(Options{
		Provider: "litellm",
		Model:    "gpt-5.6-luna",
		Providers: map[string]ProviderOverride{
			"litellm":   {APIKey: "k", BaseURL: "http://localhost:4000/v1", Format: "openai"},
			"my-anth":   {APIKey: "k", BaseURL: "https://proxy.example/anthropic", Format: "anthropic"},
			"my-gem":    {APIKey: "k", BaseURL: "https://proxy.example/gemini", Format: "gemini"},
			"legacy-ov": {APIKey: "k", BaseURL: "http://localhost:9999/v1"}, // no format → openai default
		},
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	for id, want := range map[string]sdk.Quirks{
		"litellm":   {ReasoningEffort: true},
		"my-anth":   {ThinkingObject: true, AnthropicVersion: "2023-06-01"},
		"my-gem":    {},
		"legacy-ov": {ReasoningEffort: true},
	} {
		p, err := s.Provider(id)
		if err != nil {
			t.Fatalf("provider %s: %v", id, err)
		}
		got := p.Config().Quirks
		if got.ThinkingObject != want.ThinkingObject || got.ReasoningEffort != want.ReasoningEffort ||
			got.AnthropicVersion != want.AnthropicVersion || len(got.ForceThinking) != len(want.ForceThinking) {
			t.Fatalf("%s quirks = %+v, want %+v", id, got, want)
		}
	}
}

// A built-in id keeps its registry quirks untouched when re-registered.
func TestNewSDK_BuiltInQuirksUntouched(t *testing.T) {
	s, err := NewSDK(Options{
		Provider: "openai",
		Model:    "gpt-5",
		Providers: map[string]ProviderOverride{
			"openai": {APIKey: "k"},
		},
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	p, err := s.Provider("openai")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got := p.Config().Quirks
	if !got.ReasoningEffort || got.ThinkingObject || got.AnthropicVersion != "" || len(got.ForceThinking) != 0 {
		t.Fatalf("openai quirks = %+v", got)
	}
}
