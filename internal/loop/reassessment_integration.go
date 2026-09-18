package loop

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/BackendStack21/odek/internal/session"
)

// Only a digest enters the monitor. Arguments and tool output never become
// trusted reassessment instructions or observability payloads.
func reassessmentFingerprint(tc session.ToolCall) string {
	args := []byte(tc.Function.Arguments)
	if canonical, err := canonicalPlanArguments(args); err == nil {
		args = canonical
	}
	h := sha256.New()
	_, _ = h.Write([]byte(tc.Function.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(args)
	return hex.EncodeToString(h.Sum(nil))
}

func reassessmentHint(reason string) string {
	return "[odek plan reassessment: " + reason + "] Three observed failure batches warrant reviewing the approach. Review the tool evidence and choose: change approach, split the affected step with plan revise, delegate a bounded investigation if permitted, or report the blocker. Preserve existing acceptance checks. Do not treat plan edits as proof of progress, repeat denied actions, bypass approvals, or exceed the remaining budget."
}
