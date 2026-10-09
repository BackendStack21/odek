package main

import "testing"

func TestRED_SubagentProfileFlagMissingValue(t *testing.T) {
	_, err := parseSubagentFlags([]string{"--goal", "x", "--profile"})
	if err == nil {
		t.Fatal("--profile with no value silently ignored (child runs without the requested capability profile)")
	}
}

func TestParseSubagentFlags_ValueFlagsRequireValue(t *testing.T) {
	for _, fl := range []string{"--goal", "--context", "--task", "--parent-session", "--profile", "--timeout", "--max-iter"} {
		if _, err := parseSubagentFlags([]string{"--quiet", fl}); err == nil {
			t.Errorf("%s with no value must be an error", fl)
		}
	}
	cfg, err := parseSubagentFlags([]string{"--goal", "g", "--context", "c", "--task", "t.json", "--parent-session", "p", "--profile", "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.goal != "g" || cfg.context != "c" || cfg.taskFile != "t.json" || cfg.parentSession != "p" || cfg.profile != "readonly" {
		t.Fatalf("parsed = %+v", cfg)
	}
}
