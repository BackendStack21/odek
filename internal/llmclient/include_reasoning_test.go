package llmclient

import (
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// include_reasoning defaults on for OpenAI-format custom providers, but a
// strict gateway that 400s on the field needs an escape hatch: providers.<id>
// with include_reasoning=false must dial with quirks that omit the flag.
func TestNewSDK_IncludeReasoningOptOut(t *testing.T) {
	falsy := false
	s, err := NewSDK(Options{
		Provider: "strict",
		Model:    "m",
		Providers: map[string]ProviderOverride{
			"strict": {APIKey: "k", Format: "openai", IncludeReasoning: &falsy},
		},
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	got := mustProvider(t, s, "strict").Config().Quirks
	want := sdk.Quirks{ReasoningEffort: true}
	if !quirksEqual(got, want) {
		t.Fatalf("strict quirks = %+v, want %+v (IncludeReasoning must be off)", got, want)
	}
}

// Default (unset) keeps the on-by-default behavior.
func TestNewSDK_IncludeReasoningDefaultOn(t *testing.T) {
	s, err := NewSDK(Options{
		Provider: "gw",
		Model:    "m",
		Providers: map[string]ProviderOverride{
			"gw": {APIKey: "k", Format: "openai"},
		},
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	got := mustProvider(t, s, "gw").Config().Quirks
	if !got.IncludeReasoning {
		t.Fatalf("default quirks = %+v, want IncludeReasoning on", got)
	}
}
