package main

import (
	"strings"
	"testing"
)

// ── P0-1: unknown CLI flags must never fold into the task text ──────────
//
// Regression bar for the sec-benchmark finding: `odek run --session
// --no-color --interaction-mode verbose "task"` used to prepend the
// unknown flag to the prompt — corrupting it silently and handing
// anything that controls argv (wrapper scripts, CI jobs, Makefile
// targets) a prompt-injection vector into the CLI itself.

func TestParseRunFlags_UnknownFlagBeforeTask_Errors(t *testing.T) {
	_, err := parseRunFlags([]string{"--session", "--no-color", "--interaction-mode", "verbose", "Reply with exactly the word OK"})
	if err == nil {
		t.Fatal("expected error for unknown flag --interaction-mode, got nil")
	}
	if !strings.Contains(err.Error(), "--interaction-mode") {
		t.Errorf("error should name the offending flag, got: %v", err)
	}
}

func TestParseRunFlags_UnknownFlagAfterTask_Errors(t *testing.T) {
	// Value-flags after the task used to be folded into the prompt too.
	_, err := parseRunFlags([]string{"do the thing", "--model", "gpt-5"})
	if err == nil {
		t.Fatal("expected error for unknown flag after task, got nil")
	}
	if !strings.Contains(err.Error(), "--model") {
		t.Errorf("error should name the offending flag, got: %v", err)
	}
}

func TestParseRunFlags_UnknownFlagNeverReachesTask(t *testing.T) {
	f, err := parseRunFlags([]string{"--no-color", "--bogus-flag", "real task"})
	if err == nil {
		t.Fatalf("expected error, got task=%q", f.Task)
	}
}

func TestParseRunFlags_DoubleDashPassthrough(t *testing.T) {
	f, err := parseRunFlags([]string{"--no-color", "--", "--interaction-mode", "verbose", "Reply OK"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	want := "--interaction-mode verbose Reply OK"
	if f.Task != want {
		t.Errorf("Task = %q, want %q (verbatim after --)", f.Task, want)
	}
	if f.NoColor == nil || !*f.NoColor {
		t.Error("--no-color before -- should still parse as a flag")
	}
}

func TestParseRunFlags_TrailingStandaloneFlagStillWorks(t *testing.T) {
	f, err := parseRunFlags([]string{"do the thing", "--deliver"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Task != "do the thing" {
		t.Errorf("Task = %q, want %q", f.Task, "do the thing")
	}
	if f.Deliver == nil || !*f.Deliver {
		t.Error("--deliver after task should still parse as a flag")
	}
}

func TestParseRunFlags_TaskStartingWithDashRequiresSeparator(t *testing.T) {
	if _, err := parseRunFlags([]string{"-42 is the answer"}); err == nil {
		t.Fatal("expected error for dash-prefixed task without -- separator")
	}
	f, err := parseRunFlags([]string{"--", "-42 is the answer"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.Task != "-42 is the answer" {
		t.Errorf("Task = %q, want %q", f.Task, "-42 is the answer")
	}
}

func TestParseContinueArgs_UnknownFlagErrors(t *testing.T) {
	_, _, _, err := parseContinueArgs([]string{"--interaction-mode", "verbose", "fix it"})
	if err == nil {
		t.Fatal("expected error for unknown flag in continue, got nil")
	}
	if !strings.Contains(err.Error(), "--interaction-mode") {
		t.Errorf("error should name the offending flag, got: %v", err)
	}
}

func TestParseContinueArgs_DanglingValueFlagErrors(t *testing.T) {
	// A dangling --id used to fall through and become task text.
	if _, _, _, err := parseContinueArgs([]string{"--id"}); err == nil {
		t.Fatal("expected error for dangling --id, got nil")
	}
	if _, _, _, err := parseContinueArgs([]string{"--external-ref"}); err == nil {
		t.Fatal("expected error for dangling --external-ref, got nil")
	}
}

func TestParseContinueArgs_DoubleDashPassthrough(t *testing.T) {
	_, _, task, err := parseContinueArgs([]string{"--id", "abc", "--", "--weird", "task text"})
	if err != nil {
		t.Fatalf("parseContinueArgs error: %v", err)
	}
	if task != "--weird task text" {
		t.Errorf("task = %q, want %q", task, "--weird task text")
	}
}

func TestParseReplFlags_UnknownFlagErrors(t *testing.T) {
	if _, err := parseReplFlags([]string{"--interaction-mode"}); err == nil {
		t.Fatal("expected error for unknown repl flag, got nil")
	}
	if _, err := parseReplFlags([]string{"--stream", "--nope"}); err == nil {
		t.Fatal("expected error for unknown trailing repl flag, got nil")
	}
}

// ── P0-2: `odek --version` must work like `odek version` ────────────────

func TestDispatch_VersionFlagAlias(t *testing.T) {
	for _, cmd := range []string{"--version", "-v"} {
		out := captureStdout(func() {
			if code := dispatch([]string{cmd}); code != 0 {
				t.Errorf("dispatch(%q) exit = %d, want 0", cmd, code)
			}
		})
		if !strings.Contains(out, "odek ") {
			t.Errorf("dispatch(%q) output missing version block, got:\n%s", cmd, out)
		}
	}
}
