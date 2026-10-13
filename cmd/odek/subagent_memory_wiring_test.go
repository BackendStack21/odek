package main

import (
	"os"
	"regexp"
	"testing"
)

// The line that closes the trust-laundering path is the sub-agent command
// marking its agent config as Subagent; the engine and memory tests pin the
// behaviour behind the flag, this pins that odek subagent sets it.
func TestSubagentCommandMarksAgentConfigSubagent(t *testing.T) {
	src, err := os.ReadFile("subagent.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)aCfg := odek\.Config\{.*?\n\s*Subagent:\s*true,`).Match(src) {
		t.Fatal("odek subagent must build its odek.Config with Subagent: true")
	}
}
