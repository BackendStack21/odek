package llmclient

import "strings"

// Termination is the model's outcome, separate from transport success.
type Termination string

const (
	TerminationComplete    Termination = "complete"
	TerminationTruncated   Termination = "truncated"
	TerminationRefused     Termination = "refused"
	TerminationInterrupted Termination = "interrupted"
)

func NormalizeTermination(reason string) Termination {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "stop", "end_turn", "stop_sequence", "tool_calls", "tool_use", "function_call":
		// Some compatible endpoints omit a finish reason on ordinary responses.
		return TerminationComplete
	case "length", "max_tokens", "max_output_tokens":
		return TerminationTruncated
	case "content_filter", "refusal", "safety", "recitation":
		return TerminationRefused
	default:
		return TerminationInterrupted
	}
}
