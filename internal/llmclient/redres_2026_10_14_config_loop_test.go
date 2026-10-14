package llmclient

import (
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// The SDK reports Anthropic stop reasons outside its canonical vocabulary
// (pause_turn, model_context_window_exceeded) as an empty finish reason, while
// a real end_turn is reported as "stop". An empty reason from an Anthropic
// provider therefore means the answer was not completed. OpenAI-format
// endpoints that omit a finish reason on ordinary responses stay complete.
func TestRED_AnthropicEmptyFinishReasonIsNotComplete(t *testing.T) {
	ant, err := Dial("anthropic", "claude-sonnet-4-5", "sk-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ant.mapResult(&sdk.ChatResult{Content: "partial", FinishReason: ""}).Termination; got != TerminationInterrupted {
		t.Errorf("anthropic empty finish reason = %q, want interrupted", got)
	}
	if got := ant.mapResult(&sdk.ChatResult{Content: "done", FinishReason: "stop"}).Termination; got != TerminationComplete {
		t.Errorf("anthropic stop = %q, want complete", got)
	}
	ds, err := Dial("deepseek", "deepseek-v4-flash", "sk-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ds.mapResult(&sdk.ChatResult{Content: "done", FinishReason: ""}).Termination; got != TerminationComplete {
		t.Errorf("openai-format empty finish reason = %q, want complete", got)
	}
}
