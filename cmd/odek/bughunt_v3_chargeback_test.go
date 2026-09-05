package main

// Bug-hunt v3 (fix/bughunt-v3) RED test — delegate_tasks charge-back wiring.
//
// The tool must forward a completed child's reported tokens_used into the
// parent's budget view charging hook, and tolerate views without the hook.

import (
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

// chargingView is a budget.View that also implements the charging hook.
type chargingView struct {
	budget.View
	charged int64
}

func (f *chargingView) ChargeExternalUsage(n int64) { f.charged += n }

func TestDelegateTasks_ChargeParentUsage(t *testing.T) {
	fv := &chargingView{}
	dt := &delegateTasksTool{budgetView: fv}

	dt.chargeParentUsage(1500)
	if fv.charged != 1500 {
		t.Fatalf("child usage 1500 not charged to parent view: charged = %d", fv.charged)
	}

	dt.chargeParentUsage(250)
	if fv.charged != 1750 {
		t.Fatalf("second child's usage not accumulated: charged = %d, want 1750", fv.charged)
	}

	// A view without the hook must not panic.
	dt2 := &delegateTasksTool{} // budgetView nil
	dt2.chargeParentUsage(100)

	// A plain View (no charging hook) must not panic either.
	dt3 := &delegateTasksTool{budgetView: nopBudgetView{}}
	dt3.chargeParentUsage(100)
}

type nopBudgetView struct{}

func (nopBudgetView) BudgetSnapshot() budget.Snapshot { return budget.Snapshot{} }
