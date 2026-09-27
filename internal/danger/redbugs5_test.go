package danger

import (
	"testing"
)

// Denylist prefix matching trimmed only the edges; internal whitespace runs
// ('git<space><space>push', 'git\t push') bypassed a 'git push' denylist
// entry and fell through to the class-based action.
func TestActionForCommand_DenylistInternalWhitespace(t *testing.T) {
	cfg := DangerousConfig{
		Classes:  map[RiskClass]Action{NetworkEgress: Allow},
		Denylist: []string{"git push"},
	}
	for _, cmd := range []string{"git  push origin", "git\t push origin", "git push origin"} {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %v, want Deny", cmd, got)
		}
	}
}

// SetTrustAll previously short-circuited promptLocked for every class,
// including the classes TrustShortcutAllowed explicitly excludes
// (UnreadExec, Persistence, Destructive, Blocked, Unknown, ToolBatch).
func TestTrustAll_DoesNotSkipExcludedClasses(t *testing.T) {
	a := NewTTYApprover(&DangerousConfig{})
	a.TTYPath = "/nonexistent-tty-for-test"
	a.SetTrustAll(true)
	for _, cls := range []RiskClass{UnreadExec, Persistence, Destructive, Blocked, Unknown} {
		if err := a.PromptCommand(cls, "cmd", "desc"); err == nil {
			t.Errorf("PromptCommand(%s) with trustAll returned nil, want error", cls)
		}
	}
	// Classes that DO qualify for trust shortcuts keep the skip.
	for _, cls := range []RiskClass{Safe, SystemWrite} {
		if err := a.PromptCommand(cls, "cmd", "desc"); err != nil {
			t.Errorf("PromptCommand(%s) with trustAll returned %v, want nil", cls, err)
		}
	}
}
