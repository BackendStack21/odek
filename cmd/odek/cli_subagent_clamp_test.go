package main

import (
	"strings"
	"testing"
)

// From the 2026-10-13 CLI contract audit: every command was run against
// its documented contract.

// Sub-agent caps are documented as limits; a value above them is clamped
// and the clamp is reported on stderr instead of happening silently.
func TestRED_SubagentFlags_ClampIsReported(t *testing.T) {
	flush := captureStderr(t)
	cfg, err := parseSubagentFlags([]string{"--goal", "x", "--timeout", "99999", "--max-iter", "500"})
	stderr := flush()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.timeout != 1800 || cfg.maxIter != 100 {
		t.Fatalf("clamp: timeout=%d maxIter=%d", cfg.timeout, cfg.maxIter)
	}
	if !strings.Contains(stderr, "--timeout 99999 exceeds the sub-agent cap, clamped to 1800") ||
		!strings.Contains(stderr, "--max-iter 500 exceeds the sub-agent cap, clamped to 100") {
		t.Fatalf("clamp not reported on stderr:\n%s", stderr)
	}
}
