package llmclient

import "testing"

func TestNormalizeTermination(t *testing.T) {
	for reason, want := range map[string]Termination{"stop": TerminationComplete, "tool_calls": TerminationComplete, "": TerminationComplete, "length": TerminationTruncated, "max_tokens": TerminationTruncated, "content_filter": TerminationRefused, "refusal": TerminationRefused, "pause_turn": TerminationInterrupted, "future_reason": TerminationInterrupted} {
		if got := NormalizeTermination(reason); got != want {
			t.Errorf("%q: got %s want %s", reason, got, want)
		}
	}
}
