package danger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApprover_FrictionTriggersAfterThreshold verifies that recording
// FrictionThreshold approvals of the same class inside FrictionWindow
// causes shouldFriction to return true.
func TestApprover_FrictionTriggersAfterThreshold(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 3
	a.FrictionWindow = 60 * time.Second

	for i := 0; i < 3; i++ {
		if a.shouldFriction(SystemWrite) {
			t.Errorf("shouldFriction true before threshold (i=%d)", i)
		}
		a.recordApproval(SystemWrite)
	}
	if !a.shouldFriction(SystemWrite) {
		t.Error("shouldFriction false after threshold reached")
	}
}

// TestApprover_FrictionPerClass verifies that approvals of one class
// do not trip friction for another class.
func TestApprover_FrictionPerClass(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 2
	a.FrictionWindow = 60 * time.Second

	a.recordApproval(SystemWrite)
	a.recordApproval(SystemWrite)
	if !a.shouldFriction(SystemWrite) {
		t.Error("SystemWrite should be in friction")
	}
	if a.shouldFriction(NetworkEgress) {
		t.Error("NetworkEgress should NOT be in friction (different class)")
	}
}

// TestApprover_FrictionResetsAfterWindow verifies that old approvals
// are pruned when they fall outside the window.
func TestApprover_FrictionResetsAfterWindow(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 2
	a.FrictionWindow = 10 * time.Millisecond

	a.recordApproval(SystemWrite)
	a.recordApproval(SystemWrite)
	if !a.shouldFriction(SystemWrite) {
		t.Fatal("friction should have triggered")
	}
	time.Sleep(20 * time.Millisecond)
	if a.shouldFriction(SystemWrite) {
		t.Error("friction should have reset after window expired")
	}
}

// TestApprover_FrictionDisabledWhenThresholdZero verifies the opt-out.
func TestApprover_FrictionDisabledWhenThresholdZero(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 0

	for i := 0; i < 100; i++ {
		a.recordApproval(SystemWrite)
	}
	if a.shouldFriction(SystemWrite) {
		t.Error("friction must stay off when FrictionThreshold == 0")
	}
}

// TestApprover_FrictionWarningReportsActualCount pins the friction warning
// content: the "You have approved N <class> operations" line must report
// the real number of in-window approvals, not the FrictionThreshold
// constant. Drives promptLocked with a scripted TTY file (pre-written with
// the friction-mode answer) and captures stderr around the prompt.
func TestApprover_FrictionWarningReportsActualCount(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tty-script")
	if err := os.WriteFile(script, []byte("approve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewTTYApprover(&DangerousConfig{NonInteractive: strPtr("deny")})
	a.TTYPath = script
	a.FrictionThreshold = 3
	a.FrictionWindow = time.Minute
	a.pauseFn = func(time.Duration) {} // no real 1.5s pause in tests

	// Four in-window approvals — one MORE than the threshold. The warning
	// must say 4; the stale bug printed the constant threshold (3).
	for i := 0; i < 4; i++ {
		a.recordApproval(SystemWrite)
	}

	// Capture stderr while the prompt renders.
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	perr := a.PromptCommand(SystemWrite, "rm stale.txt", "test")
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	if perr != nil {
		t.Fatalf("PromptCommand errored: %v", perr)
	}

	if !strings.Contains(string(out), "approved 4 system_write operations") {
		t.Errorf("friction warning should report the actual in-window count (4), got:\n%s", out)
	}
}

// TestApprover_RecentApprovalCountMatchesWindow verifies the counter the
// warning prints: only in-window approvals count, and the count is per-class.
func TestApprover_RecentApprovalCountMatchesWindow(t *testing.T) {
	a := NewTTYApprover(nil)
	a.FrictionThreshold = 2
	a.FrictionWindow = 10 * time.Millisecond

	if got := a.recentApprovalCount(SystemWrite); got != 0 {
		t.Errorf("recentApprovalCount = %d, want 0 (empty log)", got)
	}
	a.recordApproval(SystemWrite)
	a.recordApproval(SystemWrite)
	a.recordApproval(NetworkEgress)
	if got := a.recentApprovalCount(SystemWrite); got != 2 {
		t.Errorf("recentApprovalCount(SystemWrite) = %d, want 2", got)
	}
	if got := a.recentApprovalCount(NetworkEgress); got != 1 {
		t.Errorf("recentApprovalCount(NetworkEgress) = %d, want 1", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got := a.recentApprovalCount(SystemWrite); got != 0 {
		t.Errorf("recentApprovalCount(SystemWrite) = %d, want 0 (window expired)", got)
	}
}
