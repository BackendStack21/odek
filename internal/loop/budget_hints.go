package loop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
)

// Budget-awareness telemetry (SUB_AGENTS_IMPROVEMENTS.md M1.2): the engine
// tells the model how much of its run budget is spent, so a budgeted agent
// (sub-agents especially) can pace itself and conclude cleanly instead of
// discovering its limits by being killed.
//
// The hints are engine-trusted runtime guidance — the same trust level as
// the stall-detection corrective hints — and are deliberately NOT wrapped
// in the untrusted-content boundary.

// budgetHintThresholds are the used-fraction percentages at which hints
// fire: 50% stay-on-plan, 75% consolidate (the "~25% remaining" policy the
// sub-agent lifespan block announces), 90% final stretch. A threshold fires
// at most once per run, on whichever dimension — iterations or wall clock —
// crosses it first.
var budgetHintThresholds = [...]int{50, 75, 90}

// budgetHintState carries the per-run fired-flags so each threshold warns
// exactly once.
type budgetHintState struct {
	fired [len(budgetHintThresholds)]bool
}

// SetBudgetHints enables budget-awareness telemetry for runs of this
// engine. Sub-agents enable it via the operator subagent config section;
// top-level runs default to off so interactive behaviour is unchanged.
func (e *Engine) SetBudgetHints(on bool) { e.budgetHints = on }

// RequestFinalization asks the active run to conclude at the next iteration
// boundary: no new tool batches start and the engine produces the
// partial-progress summary prefixed with timeBudgetSummaryMarker instead of
// running to the iteration cap. Non-blocking and safe to call from any
// goroutine — typically a watcher on the caller's soft deadline. The flag
// is reset when the next run starts; a request arriving after the run
// finished is a no-op.
func (e *Engine) RequestFinalization() { e.finalizeReq.Store(true) }

// BudgetSnapshot implements budget.View: a point-in-time view of the run's
// remaining budget with the engine's cumulative token totals applied.
// Returns the zero Snapshot when no run is active or no limits are
// configured. Safe for tools to call: during a tool batch the loop
// goroutine is blocked, so there is no concurrent Checker access.
func (e *Engine) BudgetSnapshot() budget.Snapshot {
	if e == nil {
		return budget.Snapshot{}
	}
	return e.budget.Snapshot(int64(e.TotalInputTokens)+int64(e.TotalCacheReadTokens)+int64(e.TotalCacheCreationTokens), int64(e.TotalOutputTokens))
}

// ChargeExternalUsage records usage incurred OUTSIDE this engine's own LLM
// calls — a completed sub-agent's reported token spend — into the cumulative
// totals, so later BudgetSnapshot values reflect it: the share-mode passdown
// for the NEXT spawn hands out reduced headroom, and the parent's own token
// cap counts child spend instead of silently exceeding it (N parallel
// children each receiving the full pre-spawn headroom could spend up to N×
// the configured cap). Charged as input tokens: the child result envelope
// reports one combined number. Safe to call from tool goroutines during a
// batch — the loop goroutine is blocked (the same invariant BudgetSnapshot
// relies on).
func (e *Engine) ChargeExternalUsage(tokens int64) {
	if e == nil || tokens <= 0 {
		return
	}
	e.TotalInputTokens += int(tokens)
}

// budgetWarnings returns the budget-awareness hint lines for the given
// number of completed iterations (empty when no threshold newly crossed).
// Iteration thresholds use completed*100 >= t*maxIter; wall-clock
// thresholds compare elapsed runtime against the context deadline (when
// one is set) relative to the run start.
func (e *Engine) budgetWarnings(completed int, start time.Time, ctx context.Context, st *budgetHintState) []string {
	nowFn := e.budgetNow
	if nowFn == nil {
		nowFn = time.Now
	}
	var out []string
	for ti, t := range budgetHintThresholds {
		if st.fired[ti] {
			continue
		}
		crossed := false
		detail := ""
		if e.maxIter > 0 && completed*100 >= t*e.maxIter {
			crossed = true
			detail = fmt.Sprintf("%d/%d iterations", completed, e.maxIter)
		}
		if !crossed {
			if deadline, ok := ctx.Deadline(); ok && deadline.After(start) {
				elapsed := nowFn().Sub(start)
				total := deadline.Sub(start)
				if elapsed >= total*time.Duration(t)/100 {
					crossed = true
					detail = fmt.Sprintf("%s of %s elapsed",
						elapsed.Round(time.Second), total.Round(time.Second))
				}
			}
		}
		if !crossed {
			continue
		}
		st.fired[ti] = true
		var msg string
		switch t {
		case 50:
			msg = fmt.Sprintf("[budget: 50%% used (%s) — stay on plan; defer optional work.]", detail)
		case 75:
			msg = fmt.Sprintf("[budget: 75%% used (%s) — begin consolidating findings; wrap up within the remaining budget.]", detail)
		default:
			msg = fmt.Sprintf("[budget: 90%% used (%s) — final stretch: deliver your report with the next response.]", detail)
		}
		out = append(out, msg)
		e.emitSignal(SignalEvent{
			Type:   "budget_warning",
			Detail: fmt.Sprintf("threshold_%d: %s", t, detail),
		})
	}
	return out
}

// PartialSummaryReason classifies a final answer produced by one of the
// engine's budget-exhaustion paths. It returns (reason, true) when the text
// carries a partial-summary marker — "iteration_budget", "execution_budget",
// or "time_budget" — so callers (the sub-agent CLI's result contract) can
// set status/partial_reason without string-matching the markers themselves.
func PartialSummaryReason(final string) (string, bool) {
	switch {
	case strings.HasPrefix(final, budgetSummaryMarker):
		return "iteration_budget", true
	case strings.HasPrefix(final, execBudgetSummaryMarker):
		return "execution_budget", true
	case strings.HasPrefix(final, timeBudgetSummaryMarker):
		return "time_budget", true
	}
	return "", false
}
