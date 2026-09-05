package loop

// Bug-hunt v3 (fix/bughunt-v3) RED test — sub-agent usage charge-back.
//
// Share-mode budget passdown snapshotted the parent's remaining budget at
// spawn time but never charged a completed child's reported usage back, so
// N sequential children each inherited the full headroom and the parent's
// own token cap silently over-counted (total spend up to N× the cap).

import (
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
)

func TestChargeExternalUsage_ReflectsInSnapshot(t *testing.T) {
	e := &Engine{}
	e.budget = budget.NewChecker(budget.Limits{MaxInputTokens: 1000}, time.Now())

	before := e.BudgetSnapshot()
	if !before.InputTokensExhausted == false || before.RemainingInputTokens != 1000 {
		t.Fatalf("precondition: remaining input tokens = %d, want 1000", before.RemainingInputTokens)
	}

	e.ChargeExternalUsage(400)
	after := e.BudgetSnapshot()
	if got := after.RemainingInputTokens; got != 600 {
		t.Fatalf("after charging child usage 400, remaining = %d, want 600 — child spend must reduce the parent's share-mode headroom and count against its cap", got)
	}

	// Non-positive charges are no-ops.
	e.ChargeExternalUsage(0)
	e.ChargeExternalUsage(-5)
	if got := e.BudgetSnapshot().RemainingInputTokens; got != 600 {
		t.Fatalf("non-positive charge moved remaining to %d, want 600", got)
	}
}
