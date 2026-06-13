package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// promptLocalWriteConfig returns a DangerousConfig that treats local file
// writes as Prompt, so simple commands like `touch` can exercise the approval
// path deterministically without requiring sudo or network access.
func promptLocalWriteConfig() danger.DangerousConfig {
	return danger.DangerousConfig{
		Classes: map[danger.RiskClass]danger.Action{
			danger.LocalWrite: danger.Prompt,
		},
	}
}

// TestParallelShell_Danger_NilApproverNonInteractiveDeny verifies that
// parallel_shell falls back to the non-interactive deny action when no
// approver is configured and NonInteractive is set to "deny".
//
// This is a regression test for the approval bypass where parallel_shell
// would silently skip the Prompt branch when t.approver == nil and execute
// the command anyway.
func TestParallelShell_Danger_NilApproverNonInteractiveDeny(t *testing.T) {
	dc := promptLocalWriteConfig()
	dc.NonInteractive = strPtr("deny")
	tool := &parallelShellTool{dangerousConfig: dc}

	marker := t.TempDir() + "/should-not-exist"
	args := fmt.Sprintf(`{"commands":[{"command":"touch %s"}]}`, marker)

	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("Call() should return error payload, not a Go error: %v", err)
	}
	if !strings.Contains(result, "command rejected") && !strings.Contains(result, "denied") {
		t.Fatalf("expected rejection error in result, got: %s", result)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("dangerous command executed without approval (marker created)")
	}
}

// TestParallelShell_Danger_NilApproverTTYApprove verifies that parallel_shell
// falls back to a TTY-style approver when no explicit approver is configured.
// The mock TTY contains "a" (approve), so the dangerous command should run.
func TestParallelShell_Danger_NilApproverTTYApprove(t *testing.T) {
	tty, cleanup := writeTTY(t, "a")
	defer cleanup()

	tool := &parallelShellTool{
		dangerousConfig: promptLocalWriteConfig(),
		ttyPath:         tty,
	}

	marker := t.TempDir() + "/should-exist"
	args := fmt.Sprintf(`{"commands":[{"command":"touch %s"}]}`, marker)

	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if strings.Contains(result, `"error"`) {
		t.Fatalf("approved command returned error payload: %s", result)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("approved command did not run (marker missing): %v", statErr)
	}
}

// TestParallelShell_Danger_NilApproverTTYDeny verifies that parallel_shell
// falls back to a TTY-style approver when no explicit approver is configured,
// and respects a deny response.
func TestParallelShell_Danger_NilApproverTTYDeny(t *testing.T) {
	tty, cleanup := writeTTY(t, "d")
	defer cleanup()

	tool := &parallelShellTool{
		dangerousConfig: promptLocalWriteConfig(),
		ttyPath:         tty,
	}

	marker := t.TempDir() + "/should-not-exist-deny"
	args := fmt.Sprintf(`{"commands":[{"command":"touch %s"}]}`, marker)

	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("Call() should return error payload, not a Go error: %v", err)
	}
	if !strings.Contains(result, "command rejected") && !strings.Contains(result, "denied") {
		t.Fatalf("expected rejection error in result, got: %s", result)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("denied command executed anyway (marker created)")
	}
}

// TestParallelShell_Danger_MultipleCommandsPrompted verifies that every
// dangerous command in a parallel_shell batch is checked for approval.
func TestParallelShell_Danger_MultipleCommandsPrompted(t *testing.T) {
	tty, cleanup := writeTTY(t, "a") // approve both
	defer cleanup()

	tool := &parallelShellTool{
		dangerousConfig: promptLocalWriteConfig(),
		ttyPath:         tty,
	}

	dir := t.TempDir()
	args := fmt.Sprintf(`{"commands":[
		{"command":"touch %s/a"},
		{"command":"touch %s/b"}
	]}`, dir, dir)

	result, err := tool.Call(args)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}

	var r parallelShellResult
	mustUnmarshal(t, result, &r)
	if len(r.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(r.Results))
	}
	for i, entry := range r.Results {
		if entry.Error != "" {
			t.Errorf("cmd %d failed: %s", i, entry.Error)
		}
	}
	if _, err := os.Stat(dir + "/a"); err != nil {
		t.Errorf("first marker missing: %v", err)
	}
	if _, err := os.Stat(dir + "/b"); err != nil {
		t.Errorf("second marker missing: %v", err)
	}
}

// TestParallelShell_Danger_TrustedClassCached verifies that trusting a risk
// class in the TTY fallback persists across parallel_shell calls on the same
// tool instance.
func TestParallelShell_Danger_TrustedClassCached(t *testing.T) {
	tty, cleanup := writeTTY(t, "t") // trust session
	defer cleanup()

	tool := &parallelShellTool{
		dangerousConfig: promptLocalWriteConfig(),
		ttyPath:         tty,
	}

	dir := t.TempDir()

	// First call: user trusts LocalWrite.
	args1 := fmt.Sprintf(`{"commands":[{"command":"touch %s/first"}]}`, dir)
	_, err := tool.Call(args1)
	if err != nil {
		t.Fatalf("first Call() error: %v", err)
	}

	// Second call: should succeed without a TTY, since the class is cached.
	// Use a nonexistent TTY path to prove no prompt is attempted.
	tool.ttyPath = "/nonexistent/tty"
	args2 := fmt.Sprintf(`{"commands":[{"command":"touch %s/second"}]}`, dir)
	result, err := tool.Call(args2)
	if err != nil {
		t.Fatalf("second Call() (trusted) error: %v", err)
	}
	if strings.Contains(result, `"error"`) {
		t.Fatalf("trusted command returned error payload: %s", result)
	}

	if _, err := os.Stat(dir + "/first"); err != nil {
		t.Errorf("first marker missing: %v", err)
	}
	if _, err := os.Stat(dir + "/second"); err != nil {
		t.Errorf("second marker missing: %v", err)
	}
}
