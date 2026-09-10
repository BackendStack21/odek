package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

func TestInputOnlyTaskBudgetIsPreserved(t *testing.T) {
	tb := taskBudgetFromSnapshot(budget.Snapshot{MaxInputTokens: 1000, RemainingInputTokens: 700})
	if tb == nil || tb.MaxInputTokens != 700 {
		t.Fatalf("input budget lost: %+v", tb)
	}
	got := clampLimits(budget.Limits{}, tb)
	if got.MaxInputTokens != 700 {
		t.Fatalf("child input cap = %d", got.MaxInputTokens)
	}
}

func TestInputExhaustionSurvivesTaskEnvelope(t *testing.T) {
	tb := taskBudgetFromSnapshot(budget.Snapshot{MaxInputTokens: 1000, InputTokensExhausted: true})
	if tb == nil {
		t.Fatal("exhausted input-only budget disappeared")
	}
	err := exhaustedTaskBudget(tb)
	if err == nil || err.Limit != budget.LimitInputTokens {
		t.Fatalf("spawn gate = %v", err)
	}
}
