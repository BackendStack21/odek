package danger

import (
	"strings"
	"testing"
	"time"
)

// Denylist scanning must stay bounded on hostile input. A command made of
// many nested `find -exec` operands makes denyPayloads re-enter denyStage for
// every -exec position at every recursion level, so the work multiplies
// instead of staying linear in the command size.
func TestRED_DenylistFindExecBounded(t *testing.T) {
	cmd := strings.Repeat("find . -exec ", 300) + `rm {} \;`
	if len(cmd) > MaxCommandBytes {
		t.Fatalf("test command exceeds MaxCommandBytes: %d", len(cmd))
	}
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	done := make(chan Action, 1)
	start := time.Now()
	go func() { done <- cfg.ActionForCommand(cmd) }()
	select {
	case <-done:
		t.Logf("took %s", time.Since(start))
	case <-time.After(5 * time.Second):
		t.Fatalf("ActionForCommand did not return within 5s for a %d-byte command (exponential denylist scan)", len(cmd))
	}
}
