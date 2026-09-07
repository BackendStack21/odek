package llmclient

import (
	"slices"
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

func quirksEqual(got, want sdk.Quirks) bool {
	return got.ThinkingObject == want.ThinkingObject &&
		got.ReasoningEffort == want.ReasoningEffort &&
		got.AnthropicVersion == want.AnthropicVersion &&
		slices.Equal(got.ForceThinking, want.ForceThinking)
}

func mustProvider(t *testing.T, s *sdk.SDK, id string) *sdk.Provider {
	t.Helper()
	p, err := s.Provider(id)
	if err != nil {
		t.Fatalf("provider %s: %v", id, err)
	}
	return p
}

// Custom (non built-in) providers registered through NewSDK must receive
// per-format default quirks. Zero quirks silently drop ChatRequest.Thinking
// on OpenAI-format gateways (LiteLLM, OpenRouter, vLLM), so reasoning tokens
// never come back.
func TestNewSDK_DefaultQuirksForCustomProviders(t *testing.T) {
	s, err := NewSDK(Options{
		Provider: "litellm",
		Model:    "gpt-5.6-luna",
		// Selected-provider overlay (APIKey/BaseURL) is the production
		// odek.New path; quirks applied in the Providers loop must survive it.
		APIKey:  "overlay-key",
		BaseURL: "http://localhost:4000/v1",
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
		got := mustProvider(t, s, id).Config().Quirks
		if !quirksEqual(got, want) {
			t.Fatalf("%s quirks = %+v, want %+v", id, got, want)
		}
	}
	litellm := mustProvider(t, s, "litellm").Config()
	if litellm.APIKey != "overlay-key" {
		t.Fatalf("selected-provider overlay API key = %q, want overlay-key", litellm.APIKey)
	}
}

// A built-in id keeps its registry quirks when re-registered. openai's
// registry quirks match the OpenAI-format default, so this table includes
// deepseek/kimi/zai whose registry flags differ — those would change if
// format defaults leaked onto built-ins.
func TestNewSDK_BuiltInQuirksUntouched(t *testing.T) {
	ids := []string{"openai", "deepseek", "zai", "kimi", "anthropic", "gemini"}
	providers := make(map[string]ProviderOverride, len(ids))
	for _, id := range ids {
		providers[id] = ProviderOverride{APIKey: "k"}
	}
	s, err := NewSDK(Options{
		Provider:  "openai",
		Model:     "gpt-5",
		APIKey:    "overlay-key",
		Providers: providers,
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	fresh := sdk.New()
	for _, id := range ids {
		got := mustProvider(t, s, id).Config().Quirks
		want := mustProvider(t, fresh, id).Config().Quirks
		if !quirksEqual(got, want) {
			t.Errorf("%s quirks = %+v, want registry %+v", id, got, want)
		}
	}
}

func TestIsBuiltinProviderID_MatchesSDKRegistry(t *testing.T) {
	fresh := sdk.New()
	for _, id := range []string{"openai", "gemini", "deepseek", "zai", "kimi", "anthropic"} {
		if _, err := fresh.Provider(id); err != nil {
			t.Fatalf("SDK registry missing %s: %v", id, err)
		}
		if !isBuiltinProviderID(id) {
			t.Errorf("%s should be treated as built-in", id)
		}
	}
	for _, id := range []string{"litellm", "legacy", "openrouter", "local"} {
		if isBuiltinProviderID(id) {
			t.Errorf("%s must not be treated as built-in", id)
		}
	}
}

// Dial of an unknown host registers the v1 "legacy" OpenAI-format provider
// and overlays selected-provider key/URL — the same path as a custom gateway.


func TestTemperatureForModel_OmitsUnsupportedGPT6Temperature(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra", "GPT-6-ASTRA"} {
		if got := temperatureForModel(model, 0); got != 0 {
			t.Errorf("temperatureForModel(%q, 0) = %v, want SDK omit sentinel 0", model, got)
		}
	}
	if got := temperatureForModel("gpt-4o", 0); got != -1 {
		t.Errorf("temperatureForModel(gpt-4o, 0) = %v, want explicit-zero sentinel -1", got)
	}
}

func TestDial_LegacyGetsOpenAIReasoningQuirks(t *testing.T) {
	c, err := Dial("", "llama3", "local", "http://127.0.0.1:9/v1")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	got := c.Provider.Config().Quirks
	want := sdk.Quirks{ReasoningEffort: true}
	if !quirksEqual(got, want) {
		t.Fatalf("legacy quirks = %+v, want %+v", got, want)
	}
}
