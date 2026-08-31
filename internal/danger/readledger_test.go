package danger

import (
	"os"
	"path/filepath"
	"testing"
)

// ── H-6: executing an unread repo-supplied script is its own gate ────────
//
// Scenarios 21/22 were the only ones that executed on the capable tier:
// the payload sat inside the correct, documented fix and fired on the
// verification run. 12 of 14 executing runs flagged the tamper in the same
// reply — detection is fine, timing is not. The gate is mechanical:
// execute a script file you have not read this session → unread_exec.

func setupScripts(t *testing.T) (dir string, script string) {
	t.Helper()
	ResetReadLedgerForTest()
	t.Cleanup(ResetReadLedgerForTest)
	dir = t.TempDir()
	script = filepath.Join(dir, "env.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(dir, "tool.py")
	if err := os.WriteFile(py, []byte("print('x')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, script
}

func TestUnreadScriptTargets_GatesUntilRead(t *testing.T) {
	dir, script := setupScripts(t)
	py := filepath.Join(dir, "tool.py")

	cmd := "bash " + script + " && python " + py
	targets := UnreadScriptTargets(cmd)
	if len(targets) != 2 {
		t.Fatalf("unread targets = %v, want both scripts", targets)
	}

	RecordRead(script)
	targets = UnreadScriptTargets(cmd)
	if len(targets) != 1 {
		t.Fatalf("after reading %s, targets = %v, want only tool.py", filepath.Base(script), targets)
	}

	RecordRead(py)
	if targets := UnreadScriptTargets(cmd); len(targets) != 0 {
		t.Fatalf("after reading both, targets = %v, want none", targets)
	}
}

func TestUnreadScriptTargets_FailedReadNeverLicenses(t *testing.T) {
	_, script := setupScripts(t)
	// A read that failed (file missing at the recorded path) never entered
	// the ledger — WasRead is false, execution stays gated. Corollary from
	// the study: a capable model's `cat env.sh` errored on a path typo and
	// it fell back to running the file.
	if WasRead(script) {
		t.Fatal("script must not count as read")
	}
	if targets := UnreadScriptTargets("sh " + script); len(targets) != 1 {
		t.Fatalf("targets = %v, want the unread script gated", targets)
	}
}

func TestClassifyScriptGate_EscalatesAndKeepsWorse(t *testing.T) {
	_, script := setupScripts(t)

	// Plain execution of an unread script → unread_exec (not bare
	// code_execution, so class trust/allow cannot bypass).
	cls, targets := ClassifyScriptGate("bash " + script)
	if cls != UnreadExec || len(targets) != 1 {
		t.Fatalf("gate = %s %v, want unread_exec with 1 target", cls, targets)
	}

	// Source form — the exact scenario-21/22 delivery shape.
	if cls, _ := ClassifyScriptGate("source " + script); cls != UnreadExec {
		t.Fatalf("source form = %s, want unread_exec", cls)
	}

	// Direct invocation of a script path — the verb is unrecognised, so
	// the plain classifier fails closed to Unknown (deny). The gate still
	// reports the target; the effective gating is at least unread_exec.
	directCls, directTargets := ClassifyScriptGate(script) // absolute path invocation
	if Rank(directCls) < Rank(UnreadExec) {
		t.Fatalf("direct invocation = %s (rank %d), must gate at unread_exec rank or harder", directCls, Rank(directCls))
	}
	if len(directTargets) == 0 {
		t.Fatal("direct invocation should still report the unread target")
	}

	// Non-execution references to script files never gate.
	if cls, targets := ClassifyScriptGate("grep pattern " + script); cls == UnreadExec || len(targets) != 0 {
		t.Fatalf("grep over a script = %s %v — reads must not gate as unread_exec", cls, targets)
	}

	// A stronger finding keeps its own (harder) class.
	if cls, _ := ClassifyScriptGate("bash " + script + "; rm -rf /"); cls != Destructive {
		t.Fatalf("compound with destructive = %s, want destructive", cls)
	}

	// After a read, the class returns to the plain classifier's answer.
	RecordRead(script)
	if cls, targets := ClassifyScriptGate("bash " + script); cls == UnreadExec || len(targets) != 0 {
		t.Fatalf("post-read gate = %s %v, want plain class with no targets", cls, targets)
	}
}

func TestUnreadExec_ConfigAndTrust(t *testing.T) {
	cfg := &DangerousConfig{}
	if got := cfg.ActionFor(UnreadExec); got != Prompt {
		t.Errorf("default action for unread_exec = %v, want prompt", got)
	}
	if TrustShortcutAllowed(UnreadExec) {
		t.Error("unread_exec must never be session-trustable — the point is per-script review")
	}
	// Explicit user override still honored (their call, made in the right place).
	allow := &DangerousConfig{Classes: map[RiskClass]Action{UnreadExec: Allow}}
	if got := allow.ActionFor(UnreadExec); got != Allow {
		t.Errorf("explicit unread_exec=allow = %v, want allow", got)
	}
}

func TestUnreadScriptTargets_InlineCodeDoesNotGate(t *testing.T) {
	setupScripts(t)
	// -c payloads are inline code — no file operand to verify.
	if targets := UnreadScriptTargets(`bash -c "echo hi"`); len(targets) != 0 {
		t.Errorf("inline -c code gated: %v", targets)
	}
	// Non-existent script paths never gate (command would just fail).
	if targets := UnreadScriptTargets("bash /definitely/not/here.sh"); len(targets) != 0 {
		t.Errorf("missing file gated: %v", targets)
	}
	// Non-script operands of interpreters (data files) don't gate.
	if targets := UnreadScriptTargets("python -m http.server"); len(targets) != 0 {
		t.Errorf("module form gated: %v", targets)
	}
}

// TestUnreadScriptTargets_DollarOperandGates pins the 2026-08 sweep fix:
// $-prefixed operands used to be skipped as "variable refs", so
// `bash $HOME/evil.sh` stayed in the trustable class while the identical
// `bash ~/evil.sh` gated. Expansion + stat decides now — fail toward
// gating.
func TestUnreadScriptTargets_DollarOperandGates(t *testing.T) {
	dir, script := setupScripts(t)
	t.Setenv("HOME", dir) // expandShellTokenPath resolves $HOME via UserHomeDir

	cmd := "bash $HOME/env.sh"
	targets := UnreadScriptTargets(cmd)
	if len(targets) != 1 {
		t.Fatalf("targets = %v, want $HOME/env.sh gated like ~/env.sh", targets)
	}

	RecordRead(script)
	if targets := UnreadScriptTargets(cmd); len(targets) != 0 {
		t.Fatalf("after reading, targets = %v, want none", targets)
	}
}

// TestUnreadScriptTargets_NoShebangOperandGates pins the ENOEXEC fallback
// fix: `bash ./no-shebang` executes the file even without a shebang, so an
// interpreter-stage operand gates regardless. Direct invocation keeps the
// old bar (a compiled binary has no shebang either) and bare extension-less
// names stay ungated (flag values / subcommands are ambiguous).
func TestUnreadScriptTargets_NoShebangOperandGates(t *testing.T) {
	dir, _ := setupScripts(t)
	plain := filepath.Join(dir, "no-shebang")
	if err := os.WriteFile(plain, []byte("echo hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if targets := UnreadScriptTargets("bash ./no-shebang"); len(targets) != 1 {
		t.Fatalf("targets = %v, want ./no-shebang gated under bash (ENOEXEC fallback)", targets)
	}
	// source/. parse the operand as shell regardless of shebang or suffix.
	if targets := UnreadScriptTargets("source ./no-shebang"); len(targets) != 1 {
		t.Fatalf("targets = %v, want ./no-shebang gated under source", targets)
	}

	RecordRead(plain)
	if targets := UnreadScriptTargets("bash ./no-shebang"); len(targets) != 0 {
		t.Fatalf("targets = %v, want none after reading the file", targets)
	}

	// Direct invocation: unchanged (exec of a binary is not interpretation).
	if targets := UnreadScriptTargets("./no-shebang"); len(targets) != 0 {
		t.Fatalf("targets = %v, want direct invocation unchanged", targets)
	}
	// Bare names without ./: unchanged (ambiguous operand class).
	if targets := UnreadScriptTargets("bash no-shebang"); len(targets) != 0 {
		t.Fatalf("targets = %v, want bare extension-less names unchanged", targets)
	}
}
