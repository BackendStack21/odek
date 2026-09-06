package main

// Coverage v3 — clampLimits residual branches: the input-tokens-exhausted
// hard zero and the max_input_tokens narrowing (unbounded operator cap,
// parent-below-operator, parent-above-operator).

import (
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

func TestClampLimits_InputTokens(t *testing.T) {
	t.Run("nil task budget returns operator limits", func(t *testing.T) {
		op := budget.Limits{MaxInputTokens: 123}
		if got := clampLimits(op, nil); got.MaxInputTokens != 123 {
			t.Fatalf("MaxInputTokens = %d, want 123", got.MaxInputTokens)
		}
	})

	t.Run("input tokens exhausted is a hard zero", func(t *testing.T) {
		op := budget.Limits{MaxInputTokens: 5000}
		tb := &taskBudget{InputTokensExhausted: true}
		if got := clampLimits(op, tb); got.MaxInputTokens != 0 {
			t.Fatalf("MaxInputTokens = %d, want 0", got.MaxInputTokens)
		}
	})

	t.Run("task budget caps unbounded operator limit", func(t *testing.T) {
		tb := &taskBudget{MaxInputTokens: 50}
		if got := clampLimits(budget.Limits{}, tb); got.MaxInputTokens != 50 {
			t.Fatalf("MaxInputTokens = %d, want 50", got.MaxInputTokens)
		}
	})

	t.Run("task budget narrows larger operator limit", func(t *testing.T) {
		op := budget.Limits{MaxInputTokens: 100}
		tb := &taskBudget{MaxInputTokens: 50}
		if got := clampLimits(op, tb); got.MaxInputTokens != 50 {
			t.Fatalf("MaxInputTokens = %d, want 50", got.MaxInputTokens)
		}
	})

	t.Run("operator limit wins when tighter", func(t *testing.T) {
		op := budget.Limits{MaxInputTokens: 30}
		tb := &taskBudget{MaxInputTokens: 50}
		if got := clampLimits(op, tb); got.MaxInputTokens != 30 {
			t.Fatalf("MaxInputTokens = %d, want 30", got.MaxInputTokens)
		}
	})

	t.Run("prices and other dimensions untouched by input token budget", func(t *testing.T) {
		op := budget.Limits{MaxRuntimeSeconds: 10, MaxToolCalls: 5}
		tb := &taskBudget{MaxInputTokens: 99, InputTokensExhausted: true}
		got := clampLimits(op, tb)
		if got.MaxRuntimeSeconds != 10 || got.MaxToolCalls != 5 {
			t.Fatalf("unrelated dimensions narrowed: %+v", got)
		}
	})
}
