package loop

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BackendStack21/odek/internal/session"
)

// recordPlanCheckResult consumes engine outcomes, never claims inside tool
// output. Conflicting tool effects execute in transcript order, so a later
// mutation invalidates an earlier check in the same batch.
func (e *Engine) recordPlanCheckResult(epoch uint64, tc session.ToolCall, callID string, failed bool) {
	if e.planStore == nil || tc.Function.Name == "plan" {
		return
	}
	if e.planStore.MatchesCheck(tc.Function.Name, tc.Function.Arguments) {
		if failed {
			// Failed verification may itself leave partial effects. Earlier
			// checks cannot establish the state after that failure.
			e.planStore.InvalidateChecks()
		}
		if e.planStore.CheckEpoch() == epoch {
			e.planStore.RecordCheckOutcome(epoch, tc.Function.Name, tc.Function.Arguments, callID, failed)
			return
		}
	}
	fx := e.executionEffects(tc)
	if fx.unknown || len(fx.writes) > 0 {
		// A failure may leave partial effects. Invalidate conservatively even
		// when a command failed or its effects cannot be described precisely.
		e.planStore.InvalidateChecks()
	}
}

func (e *Engine) pendingPlanChecks() []string {
	if e == nil || e.planStore == nil {
		return nil
	}
	return e.planStore.PendingChecks()
}

// appendCheckNotice keeps the bounded completion nudge from becoming an
// endless retry loop while making missing evidence explicit in the final
// persisted answer. Only bounded identifiers are included, never commands.
func (e *Engine) appendCheckNotice(answer string) string {
	pending := e.pendingPlanChecks()
	if len(pending) == 0 {
		return answer
	}
	shown := pending
	if len(shown) > 8 {
		shown = shown[:8]
	}
	labels := make([]string, len(shown))
	for i, id := range shown {
		labels[i] = strconv.QuoteToASCII(id)
	}
	notice := fmt.Sprintf("\n\n[odek verification incomplete: %d declared acceptance check(s) have no current passing evidence: %s", len(pending), strings.Join(labels, ", "))
	if len(pending) > len(shown) {
		notice += ", …"
	}
	return answer + notice + ". Task completion is not verified.]"
}
