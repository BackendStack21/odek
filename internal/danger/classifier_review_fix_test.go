package danger

import "testing"

// Review follow-ups from the adversarial diff pass.

func TestReviewDeclareXDump(t *testing.T) {
	// Flag-only declare/typeset -x prints all exported variables.
	for _, cmd := range []string{"declare -x", "typeset -x"} {
		if got := Classify(cmd); got != SystemWrite {
			t.Errorf("Classify(%q) = %v, want system_write (exported-var dump)", cmd, got)
		}
	}
	// Declaring/exporting variables stays un-escalated.
	for _, cmd := range []string{"declare -x FOO=bar", "typeset -i x=5", "declare -x FOO"} {
		if got := Classify(cmd); got == SystemWrite {
			t.Errorf("Classify(%q) = system_write, want un-escalated", cmd)
		}
	}
}

func TestReviewForkBombRequiresSpawn(t *testing.T) {
	// A function body that references the name twice without any
	// pipe/ampersand spawn is not a fork bomb.
	for _, cmd := range []string{
		"x(){ echo x; echo x }; x",
		"hello(){ echo hello; echo hello }; hello",
	} {
		if got := Classify(cmd); got == Blocked {
			t.Errorf("Classify(%q) = blocked, want allowed (no spawn in body)", cmd)
		}
	}
	// Real bombs with named functions stay blocked.
	for _, cmd := range []string{
		"bomb(){ bomb|bomb& };bomb",
		"f(){ f&f& };f",
		"bomb(){ bomb & bomb }; bomb",
	} {
		if got := Classify(cmd); got != Blocked {
			t.Errorf("Classify(%q) = %v, want blocked", cmd, got)
		}
	}
}
