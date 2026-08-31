package main

// Tests for the share-mode budget-exhaustion fix (SUB_AGENTS_IMPROVEMENTS.md
// M1.5 invariant — child = min(operator cap, parent remaining)): an EXHAUSTED
// parent budget must clamp the child to a hard cap of 0 and fail the spawn
// fast with a typed budget error, while an UNCONFIGURED parent budget keeps
// the child unlimited. Before the fix, budget.Snapshot read "exhausted" and
// "unconfigured" identically (Remaining* = 0 for both), so an exhausted
// parent contributed no cap and the child inherited no bound.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

// Exhausted parent budget + unlimited operator → the child clamps to 0 on
// every exhausted dimension (never an unbounded inherit) and the spawn gate
// reports a typed budget error for the first exhausted dimension.
func TestClampLimits_ExhaustedParentYieldsExhaustedChild(t *testing.T) {
	tb := &taskBudget{
		RuntimeExhausted:   true,
		ToolCallsExhausted: true,
		CostExhausted:      true,
	}
	got := clampLimits(budget.Limits{}, tb) // unlimited operator
	if got.MaxRuntimeSeconds != 0 || got.MaxToolCalls != 0 || got.MaxCostUSD != 0 {
		t.Errorf("clamped child limits = %+v, want 0/0/0 (hard caps, never unbounded)", got)
	}
	berr := exhaustedTaskBudget(tb)
	if berr == nil {
		t.Fatal("spawn gate returned nil for an exhausted parent budget, want typed budget error")
	}
	if berr.Limit != budget.LimitRuntime {
		t.Errorf("spawn gate limit = %q, want %q (first exhausted dimension)", berr.Limit, budget.LimitRuntime)
	}
}

// An exhausted dimension clamps even a generous operator cap down to 0;
// dimensions with live parent headroom keep the min() semantics untouched.
func TestClampLimits_ExhaustedParentClampsOperatorCap(t *testing.T) {
	tb := &taskBudget{RuntimeExhausted: true, MaxToolCalls: 4}
	got := clampLimits(budget.Limits{MaxRuntimeSeconds: 300, MaxToolCalls: 9}, tb)
	if got.MaxRuntimeSeconds != 0 {
		t.Errorf("exhausted parent runtime must clamp the child to 0, got %d", got.MaxRuntimeSeconds)
	}
	if got.MaxToolCalls != 4 {
		t.Errorf("live parent headroom must still clamp (min(9, 4)), got %d", got.MaxToolCalls)
	}
	if berr := exhaustedTaskBudget(tb); berr == nil || berr.Limit != budget.LimitRuntime {
		t.Errorf("spawn gate = %v, want typed %q budget error", berr, budget.LimitRuntime)
	}
}

// No regression: a parent budget that is nil, empty, or has only live
// headroom never caps or gates the child beyond the pre-fix min() behavior.
func TestClampLimits_UnconfiguredParentKeepsUnlimitedChild(t *testing.T) {
	op := budget.Limits{MaxRuntimeSeconds: 300, MaxToolCalls: 9}
	if got := clampLimits(op, nil); got.MaxRuntimeSeconds != 300 || got.MaxToolCalls != 9 {
		t.Errorf("nil task budget = %+v, want operator limits unchanged", got)
	}
	if got := clampLimits(budget.Limits{}, &taskBudget{}); got.MaxRuntimeSeconds != 0 ||
		got.MaxToolCalls != 0 || got.MaxCostUSD != 0 {
		t.Errorf("empty task budget = %+v, want unlimited child", got)
	}
	if berr := exhaustedTaskBudget(&taskBudget{MaxToolCalls: 3}); berr != nil {
		t.Errorf("spawn gate fired on live headroom: %v", berr)
	}
}

// The task-file budget block must carry the exhaustion flags so a remaining
// of 0 is no longer wire-ambiguous with "unconfigured".
func TestTaskBudgetFromSnapshot_ExhaustedFlags(t *testing.T) {
	s := budget.Snapshot{
		MaxRuntimeSeconds:       60,
		RuntimeExhausted:        true,
		MaxToolCalls:            10,
		RemainingToolCalls:      4,
		MaxCostUSD:              1.0,
		RemainingCostUSD:        0,
		CostExhausted:           true,
	}
	got := taskBudgetFromSnapshot(s)
	if got == nil {
		t.Fatal("exhausted snapshot → nil task budget, want the flags carried")
	}
	if !got.RuntimeExhausted || !got.CostExhausted {
		t.Errorf("flags runtime=%v cost=%v, want true/true", got.RuntimeExhausted, got.CostExhausted)
	}
	if got.MaxToolCalls != 4 || got.ToolCallsExhausted {
		t.Errorf("tool calls = %d exhausted=%v, want 4/false", got.MaxToolCalls, got.ToolCallsExhausted)
	}
}

// A fully unconfigured parent (zero snapshot) still maps to nil — the child
// keeps its operator caps (no regression).
func TestTaskBudgetFromSnapshot_ZeroSnapshotStillNil(t *testing.T) {
	if got := taskBudgetFromSnapshot(budget.Snapshot{}); got != nil {
		t.Errorf("zero snapshot → %+v, want nil", got)
	}
}

// The spawn gate maps each exhausted dimension to the typed budget error
// with the matching limit name, and stays silent for live headroom.
func TestExhaustedTaskBudget_SpawnGate(t *testing.T) {
	if exhaustedTaskBudget(nil) != nil {
		t.Error("nil task budget must not trip the spawn gate")
	}
	cases := []struct {
		tb    *taskBudget
		limit string
	}{
		{&taskBudget{RuntimeExhausted: true}, budget.LimitRuntime},
		{&taskBudget{ToolCallsExhausted: true}, budget.LimitToolCalls},
		{&taskBudget{CostExhausted: true}, budget.LimitCostUSD},
	}
	for _, tc := range cases {
		berr := exhaustedTaskBudget(tc.tb)
		if berr == nil {
			t.Fatalf("taskBudget %+v → nil, want typed budget error", tc.tb)
		}
		if berr.Limit != tc.limit {
			t.Errorf("limit = %q, want %q", berr.Limit, tc.limit)
		}
		var typed *budget.Error
		if !errors.As(berr, &typed) {
			t.Errorf("%v is not a *budget.Error", berr)
		}
	}
}

// A pre-run budget stop surfaces the documented budget contract — exit code
// 4 with a budget_exhausted result envelope — same as a mid-run exhaustion.
func TestSubagentExit_TypedBudgetErrorExits4(t *testing.T) {
	err := fmt.Errorf("parent budget exhausted before start: %w", &budget.Error{Limit: budget.LimitRuntime})
	if code := subagentExit(err); code != 4 {
		t.Errorf("subagentExit = %d, want 4", code)
	}
}
