package main

import (
	"fmt"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

// TestParseRunFlags_BudgetFlags covers the WP6 execution-budget flags.
func TestParseRunFlags_BudgetFlags(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--max-runtime", "300",
		"--max-tool-calls", "50",
		"--max-input-tokens", "100000",
		"--max-output-tokens", "20000",
		"--max-cost-usd", "1.5",
		"do the task",
	})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if f.MaxRuntime != 300 || f.MaxToolCalls != 50 || f.MaxInputTokens != 100000 ||
		f.MaxOutputTokens != 20000 || f.MaxCostUSD != 1.5 {
		t.Errorf("budget flags not parsed: %+v", f)
	}
	if f.Task != "do the task" {
		t.Errorf("Task = %q, want %q", f.Task, "do the task")
	}
}

// TestParseRunFlags_BudgetFlagsInvalid: non-positive/garbage values are a
// clear startup error, not a silently ignored budget.
func TestParseRunFlags_BudgetFlagsInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"--max-runtime", "0"},
		{"--max-runtime", "-5"},
		{"--max-runtime", "abc"},
		{"--max-tool-calls", "-1"},
		{"--max-input-tokens", "0"},
		{"--max-output-tokens", "x"},
		{"--max-cost-usd", "-0.5"},
		{"--max-runtime"}, // missing value
	} {
		if _, err := parseRunFlags(args); err == nil {
			t.Errorf("parseRunFlags(%v) should fail", args)
		}
	}
}

// TestRunExit_BudgetExitCode pins the dispatch mapping: a typed budget.Error
// exits 4 (odek-extension/v1), other errors keep exit 1, success exits 0.
func TestRunExit_BudgetExitCode(t *testing.T) {
	if code := runExit(nil); code != 0 {
		t.Errorf("runExit(nil) = %d, want 0", code)
	}
	berr := &budget.Error{Limit: budget.LimitToolCalls, Observed: 6, Maximum: 5}
	if code := runExit(berr); code != 4 {
		t.Errorf("runExit(budget.Error) = %d, want 4", code)
	}
	// Wrapped budget errors still map to 4.
	if code := runExit(fmt.Errorf("iteration 2: %w", berr)); code != 4 {
		t.Errorf("runExit(wrapped budget.Error) = %d, want 4", code)
	}
	if code := runExit(fmt.Errorf("model exploded")); code != 1 {
		t.Errorf("runExit(other) = %d, want 1", code)
	}
}
