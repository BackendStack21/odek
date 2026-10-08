package main

import (
	"strings"
	"testing"
)

// ── unknown CLI flags must never fold into the task text ──────────
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
	_, _, err := parseContinueArgs([]string{"--interaction-mode", "verbose", "fix it"})
	if err == nil {
		t.Fatal("expected error for unknown flag in continue, got nil")
	}
	if !strings.Contains(err.Error(), "--interaction-mode") {
		t.Errorf("error should name the offending flag, got: %v", err)
	}
}

func TestParseContinueArgs_DanglingValueFlagErrors(t *testing.T) {
	// A dangling --id used to fall through and become task text.
	if _, _, err := parseContinueArgs([]string{"--id"}); err == nil {
		t.Fatal("expected error for dangling --id, got nil")
	}
	if _, _, err := parseContinueArgs([]string{"--external-ref"}); err == nil {
		t.Fatal("expected error for dangling --external-ref, got nil")
	}
}

func TestParseContinueArgs_DoubleDashPassthrough(t *testing.T) {
	_, f, err := parseContinueArgs([]string{"--id", "abc", "--", "--weird", "task text"})
	if err != nil {
		t.Fatalf("parseContinueArgs error: %v", err)
	}
	if f.Task != "--weird task text" {
		t.Errorf("task = %q, want %q", f.Task, "--weird task text")
	}
	// After "--" even a pinned-flag name is task text.
	_, f, err = parseContinueArgs([]string{"--", "--model", "is the question"})
	if err != nil || f.Task != "--model is the question" {
		t.Fatalf("task = %q, err = %v", f.Task, err)
	}
}

// continue accepts the run flags that shape one turn, in any order before
// the task, with --id anywhere among them.
func TestParseContinueArgs_AcceptsTurnFlags(t *testing.T) {
	id, f, err := parseContinueArgs([]string{
		"--no-color", "--no-stream", "--events-jsonl", "/tmp/ev.jsonl", "--events-include-args",
		"--max-iter", "5", "--id", "abc", "--thinking", "low", "--ctx", "a.go,b.go",
		"--tool", "shell", "--no-tool", "browser", "--max-tool-calls", "3", "--max-cost-usd", "0.5",
		"--no-compaction", "--no-planning", "Describe branch changes?",
	})
	if err != nil {
		t.Fatalf("parseContinueArgs error: %v", err)
	}
	if id != "abc" || f.Task != "Describe branch changes?" {
		t.Fatalf("id = %q, task = %q", id, f.Task)
	}
	if f.NoColor == nil || !*f.NoColor || f.Stream == nil || *f.Stream {
		t.Fatalf("presentation flags not parsed: NoColor=%v Stream=%v", f.NoColor, f.Stream)
	}
	if f.EventsJSONL != "/tmp/ev.jsonl" || f.EventsIncludeArgs == nil || !*f.EventsIncludeArgs {
		t.Fatalf("event flags not parsed: %q %v", f.EventsJSONL, f.EventsIncludeArgs)
	}
	if f.MaxIter != 5 || f.Thinking != "low" || f.MaxToolCalls != 3 || f.MaxCostUSD != 0.5 {
		t.Fatalf("turn flags not parsed: %+v", f)
	}
	if len(f.Ctx) != 2 || len(f.ToolsEnabled) != 1 || len(f.ToolsDisabled) != 1 {
		t.Fatalf("ctx/tool flags not parsed: ctx=%v on=%v off=%v", f.Ctx, f.ToolsEnabled, f.ToolsDisabled)
	}
	if f.Compaction == nil || *f.Compaction || f.Planning == nil || *f.Planning {
		t.Fatalf("compaction/planning flags not parsed: %v %v", f.Compaction, f.Planning)
	}
}

// Flags that would change what the session pins are refused by name, never
// silently ignored and never folded into the task text.
func TestParseContinueArgs_RejectsPinnedFlags(t *testing.T) {
	cases := [][]string{
		{"--model", "x", "task"},
		{"--provider", "x", "task"},
		{"--base-url", "http://x", "task"},
		{"--system", "be nice", "task"},
		{"--sandbox", "task"},
		{"--no-sandbox", "task"},
		{"--sandbox-image", "alpine", "task"},
		{"--sandbox-network", "none", "task"},
		{"--sandbox-readonly", "task"},
		{"--sandbox-memory", "1g", "task"},
		{"--sandbox-cpus", "1", "task"},
		{"--sandbox-user", "1000", "task"},
		{"--session", "task"},
		{"--id", "abc", "--no-color", "--model", "x", "task"},
	}
	for _, args := range cases {
		_, _, err := parseContinueArgs(args)
		if err == nil {
			t.Errorf("%v: expected rejection, got nil", args)
			continue
		}
		flag := args[0]
		if args[0] == "--id" {
			flag = "--model"
		}
		if !strings.Contains(err.Error(), flag) || !strings.Contains(err.Error(), "odek continue") {
			t.Errorf("%v: error must name the flag and the command, got: %v", args, err)
		}
	}
	// --deliver is a per-turn flag and continue implements it.
	if _, f, err := parseContinueArgs([]string{"--deliver", "task"}); err != nil || f.Deliver == nil || !*f.Deliver {
		t.Fatalf("--deliver must be accepted: err=%v f=%+v", err, f.Deliver)
	}
}

// --id belongs to continue; the run parser accepts the token so continue
// can share it, and parseContinueArgs lifts it out of the flags.
func TestParseRunFlags_IDIsLifted(t *testing.T) {
	f, err := parseRunFlags([]string{"--id", "abc", "task"})
	if err != nil || f.SessionID != "abc" || f.Task != "task" {
		t.Fatalf("run parser: err=%v id=%q task=%q", err, f.SessionID, f.Task)
	}
	id, cf, err := parseContinueArgs([]string{"--id", "abc", "task"})
	if err != nil || id != "abc" || cf.SessionID != "" {
		t.Fatalf("continue parser: err=%v id=%q leftover=%q", err, id, cf.SessionID)
	}
	if _, _, err := parseContinueArgs([]string{"--id"}); err == nil {
		t.Fatal("dangling --id must error")
	}
	// A dangling task-less continue reports its own message.
	if _, _, err := parseContinueArgs([]string{"--id", "abc"}); err == nil || !strings.Contains(err.Error(), "for continue") {
		t.Fatalf("task-less continue error = %v", err)
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

// ── `odek --version` must work like `odek version` ────────────────

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
