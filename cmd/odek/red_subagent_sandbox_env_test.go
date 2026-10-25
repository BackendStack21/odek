package main

import (
	"os"
	"strings"
	"testing"
)

// A sandboxed sub-agent is told its execution environment the same way the
// parent is: its system prompt gets the block for the child's own container,
// after setupSandbox and before the engine is configured.
func TestRED_Subagent_SystemPromptCarriesSandboxEnvironment(t *testing.T) {
	src, err := os.ReadFile("subagent.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	setup := strings.Index(s, "setupSandbox(tools, sbCfg)")
	use := strings.Index(s, "SystemMessage:    systemMsg,")
	if setup < 0 || use < 0 {
		t.Fatal("subagent.go layout changed: setupSandbox call or SystemMessage wiring not found")
	}
	if !strings.Contains(s[setup:use], "systemMsg = withSandboxEnvironment(systemMsg, ") {
		t.Error("the sub-agent system prompt must get withSandboxEnvironment for its container between setupSandbox and the engine config")
	}
}
