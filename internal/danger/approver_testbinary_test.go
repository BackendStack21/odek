package danger

import (
	"strings"
	"testing"
	"time"
)

// TestTTYApprover_TestBinaryFailsClosed verifies that a TTYApprover running
// inside a test binary WITHOUT a fixture TTY denies operations instead of
// opening /dev/tty. In a background go test process the /dev/tty open
// succeeds on some platforms (macOS) but the subsequent read raises SIGTTIN,
// stopping the whole test binary — a hang, not a failure. The approver must
// fail closed: return a denial error promptly. Fixture-driven tests
// (ttyPathForTest / TTYPath override) are unaffected.
func TestTTYApprover_TestBinaryFailsClosed(t *testing.T) {
	// Real construction path: TTYPath = /dev/tty, no DangerousConfig.
	a := NewTTYApprover(nil)

	done := make(chan error, 1)
	go func() {
		done <- a.PromptCommand(SystemWrite, "cat /etc/shadow", "read_file")
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected denial in test binary without fixture TTY, got approval")
		}
		if !strings.Contains(err.Error(), "denied") {
			t.Fatalf("expected denial error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TTYApprover blocked for 5s in test binary — must fail closed instead of reading /dev/tty")
	}
}

// TestZeroValueApprover_DoesNotFailOpen: a zero-value TTYApprover (no TTY
// path, no NonInteractive config) must never approve. Silent approval is
// the worst possible default for a security gate.
func TestZeroValueApprover_DoesNotFailOpen(t *testing.T) {
	var a TTYApprover
	if err := a.PromptCommand(SystemWrite, "cat /etc/shadow", "read_file"); err == nil {
		t.Fatal("zero-value TTYApprover approved a system_write operation — fail-open default")
	}
}
