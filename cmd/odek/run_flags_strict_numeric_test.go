package main

import "testing"

func TestParseRunFlags_StrictNumericFlags(t *testing.T) {
	bad := [][]string{
		{"--max-runtime", "5m"},
		{"--max-runtime", "0"},
		{"--max-runtime", "-3"},
		{"--max-runtime", "10s"},
		{"--max-tool-calls", "5x"},
		{"--max-input-tokens", "1e3"},
		{"--max-output-tokens", "12 "},
		{"--max-cost-usd", "2.5usd"},
		{"--max-cost-usd", "NaN"},
		{"--max-cost-usd", "Inf"},
		{"--max-cost-usd", "0"},
		{"--guard-threshold", "abc"},
		{"--guard-threshold", "0.9x"},
		{"--guard-threshold", "-1"},
		{"--guard-timeout", "5s"},
		{"--guard-timeout", ""},
		{"--memory-extended-max-size-mb", "10MB"},
		{"--memory-extended-atom-max-chars", "x"},
		{"--memory-extended-memory-budget-chars", "-4"},
		{"--memory-extended-user-state-turn-interval", "2.5"},
		{"--memory-extended-user-state-max-pending", "many"},
		{"--memory-extended-association-semantic-top-k", "3k"},
		{"--thinking-budget", "4k"},
	}
	for _, b := range bad {
		if _, err := parseRunFlags(append(append([]string{}, b...), "do it")); err == nil {
			t.Errorf("%v: expected an error", b)
		}
	}
}

func TestParseRunFlags_ValidNumericFlags(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--max-runtime", "300", "--max-tool-calls", "50", "--max-input-tokens", "1000",
		"--max-output-tokens", "2000", "--max-cost-usd", "0.25",
		"--guard-threshold", "0.8", "--guard-timeout", "7",
		"--memory-extended-max-size-mb", "64", "--thinking-budget", "2048", "do it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.MaxRuntime != 300 || f.MaxToolCalls != 50 || f.MaxInputTokens != 1000 || f.MaxOutputTokens != 2000 || f.MaxCostUSD != 0.25 {
		t.Fatalf("budget flags = %+v", f)
	}
	if f.GuardThreshold != 0.8 || f.GuardTimeoutSeconds != 7 || f.MemoryExtendedMaxSizeMB != 64 || f.ThinkingBudget != 2048 {
		t.Fatalf("other flags = %+v", f)
	}
	if f.Task != "do it" {
		t.Fatalf("task = %q", f.Task)
	}
}
