package main

import "testing"

func TestRED_MaxRuntimeSuffixNotMisparsed(t *testing.T) {
	// "5m" must not silently become 5 seconds.
	f, err := parseRunFlags([]string{"--max-runtime", "5m", "do it"})
	if err == nil {
		t.Fatalf("--max-runtime 5m accepted and parsed as %d seconds; want an error", f.MaxRuntime)
	}
}

func TestRED_MaxCostTrailingGarbageRejected(t *testing.T) {
	f, err := parseRunFlags([]string{"--max-cost-usd", "2.5usd", "do it"})
	if err == nil {
		t.Fatalf("--max-cost-usd 2.5usd accepted as %v; want an error", f.MaxCostUSD)
	}
}

func TestRED_GuardThresholdGarbage(t *testing.T) {
	f, err := parseRunFlags([]string{"--guard-threshold", "abc", "do it"})
	if err == nil {
		t.Fatalf("--guard-threshold abc accepted (threshold=%v)", f.GuardThreshold)
	}
}
