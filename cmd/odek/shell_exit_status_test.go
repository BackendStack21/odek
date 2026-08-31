package main

import (
	"strings"
	"testing"
)

// Bug-sweep 2026-08-31: a failing command that produced output returned
// (output, nil) — the exit status was silently dropped, so a failing test
// run or build was indistinguishable from a passing one. The tool's own
// comment stated the intent (surface the failure reason) the code did not
// implement. parallel_shell already reports exit_code per command.

func TestShellTool_ReportsExitStatusWithOutput(t *testing.T) {
	st := &shellTool{}
	out, err := st.Call(`{"command":"echo hello; exit 3"}`)
	if err != nil {
		t.Fatalf("expected annotated output for a failing command with stdout, got error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("stdout content lost: %q", out)
	}
	if !strings.Contains(out, "exit status 3") {
		t.Errorf("exit status not surfaced in tool output: %q", out)
	}
}

func TestShellTool_ReportsExitStatusWithStderrOnly(t *testing.T) {
	st := &shellTool{}
	// stderr present, exit 1: stderr stays visible AND the failure is named.
	out, err := st.Call(`{"command":"echo boom >&2; exit 1"}`)
	if err != nil {
		t.Fatalf("expected annotated output, got error: %v", err)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("stderr content lost: %q", out)
	}
	if !strings.Contains(out, "exit status 1") {
		t.Errorf("exit status not surfaced: %q", out)
	}
}

func TestShellTool_FailingCommandWithoutOutputStaysError(t *testing.T) {
	st := &shellTool{}
	// No output at all: the error return remains the failure channel.
	out, err := st.Call(`{"command":"exit 7"}`)
	if err == nil {
		t.Fatalf("expected error for silent failing command, got output %q", out)
	}
	if !strings.Contains(err.Error(), "exit status 7") {
		t.Errorf("error should carry the exit status, got: %v", err)
	}
}
