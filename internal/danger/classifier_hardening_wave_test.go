package danger

import (
	"strings"
	"testing"
	"time"
)

// Hardening wave driven by the adversarial review of the danger classifier:
// env-dump builtins, dynamic substitutions in write verbs, clobber redirects,
// generalized fork bombs, double-quote escape desync, recursion depth, and
// the allowlist bypassing the raw-blocked floor.

func TestHardeningEnvDumpBuiltins(t *testing.T) {
	dumps := []string{
		"set",
		"export -p",
		"declare -p",
		"typeset -p",
		"set -o", // prints shell options; still a dump-style builtin invocation
	}
	for _, cmd := range dumps {
		if got := Classify(cmd); got != SystemWrite {
			t.Errorf("Classify(%q) = %v, want system_write (env/option dump)", cmd, got)
		}
	}
	// Setting variables/options must stay un-escalated.
	keeps := []string{"export FOO=bar", "set -e", "declare -i x=5"}
	for _, cmd := range keeps {
		if got := Classify(cmd); got == SystemWrite {
			t.Errorf("Classify(%q) = system_write, want un-escalated", cmd)
		}
	}
}

func TestHardeningDynamicSubstWriteVerb(t *testing.T) {
	cases := []string{
		"chmod $(cat m) /tmp/x",
		"ln -sf $(cat t) ~/.zshenv",
		"cp $(cat src) /etc/cron.d/x",
		"tee /etc/hosts < $(cat f)",
	}
	for _, cmd := range cases {
		if got := Classify(cmd); Rank(got) < Rank(Unknown) {
			t.Errorf("Classify(%q) = %v, want fail-closed (unknown or worse)", cmd, got)
		}
	}
}

func TestHardeningClobberRedirects(t *testing.T) {
	cases := []string{
		"printf x >| /etc/hosts",
		"echo hi >| ~/.zshenv",
	}
	for _, cmd := range cases {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %v, want system_write (clobber redirect)", cmd, got)
		}
	}
}

func TestHardeningGeneralizedForkBomb(t *testing.T) {
	cases := []string{
		":(){ :|:& };:",
		"bomb(){ bomb|bomb& };bomb",
		"bomb () { bomb | bomb & } ; bomb",
		"f(){ f&f& };f",
	}
	for _, cmd := range cases {
		if got := Classify(cmd); got != Blocked {
			t.Errorf("Classify(%q) = %v, want blocked", cmd, got)
		}
	}
	// Innocent lookalikes stay unblocked.
	innocent := []string{
		"echo :{a}:",
		"bomb(){ echo hi };bomb",
		"echo bomb(){ bomb|bomb& };bomb",
	}
	for _, cmd := range innocent {
		if got := Classify(cmd); got == Blocked {
			t.Errorf("Classify(%q) = blocked, want allowed shape", cmd)
		}
	}
}

func TestHardeningDoubleQuoteEscapeDesync(t *testing.T) {
	// Inside double quotes \" is an escaped literal quote — it must not
	// toggle the double-quote state, or a later single-quote span can hide
	// a substitution body from extraction.
	cmd := `arg "b \"c\" '$(touch /tmp/pwned)'"`
	_, subs := extractSubstitutions(cmd)
	found := false
	for _, s := range subs {
		if strings.Contains(s, "touch /tmp/pwned") {
			found = true
		}
	}
	if !found {
		t.Fatalf("extractSubstitutions(%q) missed the substitution body; subs = %q", cmd, subs)
	}
}

func TestHardeningNestedSubstitutionDepthCap(t *testing.T) {
	deep := strings.Repeat("$(", 5000) + "echo hi" + strings.Repeat(")", 5000)
	done := make(chan RiskClass, 1)
	go func() { done <- Classify(deep) }()
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("Classify did not terminate on deeply nested substitutions")
	case got := <-done:
		// Past the substitution-depth cap the classifier fails closed.
		if Rank(got) < Rank(Unknown) {
			t.Errorf("Classify(deep nesting) = %v, want unknown (depth cap fail-closed)", got)
		}
	}
	// Reasonable nesting still classifies normally.
	if got := Classify("echo $(echo $(echo hi))"); got == Unknown {
		t.Error("Classify(3-deep nesting) = unknown, want normal classification")
	}
}

func TestHardeningTopMonitorSafe(t *testing.T) {
	for _, cmd := range []string{"top", "top -b -n 1"} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %v, want safe (read-only process monitor)", cmd, got)
		}
	}
}

func TestHardeningAllowlistDoesNotBypassRawBlocked(t *testing.T) {
	cfg := &DangerousConfig{
		Allowlist: []string{":(){ :|:& };:"},
		DefaultAction: func() *string {
			s := "prompt"
			return &s
		}(),
	}
	if got := cfg.ActionForCommand(":(){ :|:& };:"); got == Allow {
		t.Fatal("allowlisted fork bomb must not return Allow; the raw-blocked floor must run first")
	}
}
