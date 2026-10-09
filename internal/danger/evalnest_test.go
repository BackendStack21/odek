package danger

import (
	"strings"
	"testing"
	"time"
)

// A 120-byte `eval eval eval ...` chain makes the denylist scan exponential:
// denyStage re-scans the eval operand through denyPayloads and again through
// the unwrapped inner command at every level.
func TestRED_DenylistEvalChainBounded(t *testing.T) {
	cmd := strings.Repeat("eval ", 24) + "echo hi"
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	done := make(chan Action, 1)
	go func() { done <- cfg.ActionForCommand(cmd) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("ActionForCommand did not return within 5s for a %d-byte eval chain (exponential denylist scan)", len(cmd))
	}
}

// Memoizing repeated scans must not hide a denied command at the bottom of a
// deep chain, nor one reached through a known variable.
func TestDenylistDeepChainsStillMatch(t *testing.T) {
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		strings.Repeat("eval ", 24) + "git push origin",
		strings.Repeat("find . -exec ", 40) + `git push \;`,
		"g=git; eval eval $g push",
		"g=git; h=push; eval 'eval \"$g $h\"'",
	} {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s, want deny", cmd, got)
		}
	}
}
