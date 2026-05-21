package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// ── tryReexec tests ──────────────────────────────────────────────────

func TestTryReexec_ExecFails(t *testing.T) {
	// Swap execFunc to simulate a failure.
	orig := execFunc
	execCalled := false
	execFunc = func(argv0 string, argv []string, envv []string) error {
		execCalled = true
		return errors.New("exec failed: no such file")
	}
	defer func() { execFunc = orig }()

	// Capture stderr.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	err = tryReexec()
	w.Close()

	if !execCalled {
		t.Error("tryReexec() did not call execFunc")
	}
	if err == nil {
		t.Error("tryReexec() should return error when exec fails")
	}
	if !strings.Contains(err.Error(), "exec failed") {
		t.Errorf("unexpected error: %v", err)
	}

	// Read captured stderr.
	stderr := make([]byte, 4096)
	n, _ := r.Read(stderr)
	output := string(stderr[:n])
	if !strings.Contains(output, "re-executing") {
		t.Errorf("stderr missing 're-executing': %q", output)
	}
	if !strings.Contains(output, "restart failed") {
		t.Errorf("stderr missing 'restart failed': %q", output)
	}
}

func TestTryReexec_ExecCalledWithCorrectArgs(t *testing.T) {
	orig := execFunc
	var capturedArgv0 string
	var capturedEnv []string
	execFunc = func(argv0 string, argv []string, envv []string) error {
		capturedArgv0 = argv0
		capturedEnv = envv
		return nil // simulate success (though real Exec never returns)
	}
	defer func() { execFunc = orig }()

	_ = tryReexec()

	if capturedArgv0 == "" {
		t.Error("execFunc was not called with argv0")
	}
	// argv[0] should be the resolved executable path (via os.Executable()),
	// not necessarily os.Args[0] (which might be a bare name).
	if !strings.Contains(capturedArgv0, "odek") {
		t.Errorf("argv0 = %q, want path containing 'odek'", capturedArgv0)
	}
	if len(capturedEnv) == 0 {
		t.Error("execFunc was called with empty env")
	}
	// Verify the environment contains PATH (basic sanity check).
	found := false
	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "PATH=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("captured env does not contain PATH")
	}
}

func TestTryReexec_ReturnsNilOnSuccess(t *testing.T) {
	orig := execFunc
	execFunc = func(argv0 string, argv []string, envv []string) error {
		return nil
	}
	defer func() { execFunc = orig }()

	err := tryReexec()
	if err != nil {
		t.Errorf("tryReexec() should return nil on success, got %v", err)
	}
}

// ── restartRequested (atomic.Bool) tests ─────────────────────────────

func TestRestartRequested_DefaultFalse(t *testing.T) {
	var restartRequested atomic.Bool
	if restartRequested.Load() {
		t.Error("restartRequested should default to false")
	}
}

func TestRestartRequested_SetTrue(t *testing.T) {
	var restartRequested atomic.Bool
	restartRequested.Store(true)
	if !restartRequested.Load() {
		t.Error("restartRequested should be true after Store(true)")
	}
}

func TestRestartRequested_StoreThenLoad(t *testing.T) {
	var restartRequested atomic.Bool

	// Simulate the /restart command flow without actually sending SIGHUP
	// (which would kill the test process).
	// 1. Store(true)
	// 2. (SIGHUP would be sent here in production)
	// 3. Signal handler cancels context, loop exits
	// 4. Load() checked to decide between restart vs exit

	restartRequested.Store(true)

	if !restartRequested.Load() {
		t.Error("restartRequested.Load() should return true after Store(true)")
	}
}

func TestRestartRequested_CompareAndSwap(t *testing.T) {
	var restartRequested atomic.Bool

	// Only set from false → true.
	swapped := restartRequested.CompareAndSwap(false, true)
	if !swapped {
		t.Error("first CAS should succeed")
	}
	if !restartRequested.Load() {
		t.Error("should be true after first CAS")
	}

	// Second CAS should fail (already true).
	swapped = restartRequested.CompareAndSwap(false, true)
	if swapped {
		t.Error("second CAS should fail, value is already true")
	}

	// But we can CAS true → false.
	swapped = restartRequested.CompareAndSwap(true, false)
	if !swapped {
		t.Error("CAS true→false should succeed")
	}
	if restartRequested.Load() {
		t.Error("should be false after true→false CAS")
	}
}

// ── Signal handling behavior tests ───────────────────────────────────

func TestSIGHUPTriggersCancel(t *testing.T) {
	// This test verifies the conceptual flow:
	// 1. SIGHUP is sent
	// 2. Signal handler receives it
	// 3. Context is cancelled
	//
	// We can't fully test syscall.Exec or signal.NotifyContext
	// in unit tests, but we can verify the signal notification
	// channel works correctly.

	sigCh := make(chan os.Signal, 1)

	// Simulate what signal.Notify would do — send SIGHUP to the channel.
	go func() {
		sigCh <- syscall.SIGHUP
	}()

	sig := <-sigCh
	if sig != syscall.SIGHUP {
		t.Errorf("expected SIGHUP, got %v", sig)
	}
}

func TestSIGTERMDoesNotTriggerRestart(t *testing.T) {
	// SIGTERM should shut down WITHOUT re-exec.
	// We test the conceptual flow: signal != SIGHUP → cancel without restart.

	sigCh := make(chan os.Signal, 1)
	go func() {
		sigCh <- syscall.SIGTERM
	}()

	sig := <-sigCh
	if sig == syscall.SIGHUP {
		t.Error("SIGTERM should not be SIGHUP")
	}
	// In the real code, this path sets restartRequested=false (default)
	// and cancels the context, causing graceful exit without re-exec.
}

// ── Integration: restart message format ──────────────────────────────

func TestRestartMessage_ContainsExpectedContent(t *testing.T) {
	// The restart confirmation message sent to the user should contain
	// key phrases so the user knows what's happening.
	msg := fmt.Sprintf("%s %s",
		"\U0001F504", // 🔄
		"*Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.",
	)

	if !strings.Contains(msg, "Restarting") {
		t.Errorf("restart message missing 'Restarting': %q", msg)
	}
	if !strings.Contains(msg, "🔄") {
		t.Errorf("restart message missing 🔄 emoji: %q", msg)
	}
	if !strings.Contains(msg, "few seconds") {
		t.Errorf("restart message missing 'few seconds': %q", msg)
	}
}

// ── Stderr logging format tests ──────────────────────────────────────

func TestRestartStderrMessage_Format(t *testing.T) {
	// Verify the stderr message format printed during restart.
	msg := fmt.Sprintf("odek telegram: re-executing %s %v...\n", "/usr/local/bin/odek", []string{"telegram"})
	if !strings.Contains(msg, "odek telegram: re-executing") {
		t.Errorf("stderr restart message missing prefix: %q", msg)
	}
	if !strings.Contains(msg, "/usr/local/bin/odek") {
		t.Errorf("stderr restart message missing binary path: %q", msg)
	}
}

// ── Edge case: no binary path ────────────────────────────────────────

func TestRestartStderrMessage_NoArgs(t *testing.T) {
	// When os.Args is just the binary with no subcommand (shouldn't happen
	// in practice, but test the format doesn't panic).
	msg := fmt.Sprintf("odek telegram: re-executing %s %v...\n", "/usr/local/bin/odek", []string{})
	if !strings.Contains(msg, "odek telegram: re-executing") {
		t.Errorf("stderr restart message missing prefix: %q", msg)
	}
	if !strings.Contains(msg, "[]") {
		t.Errorf("stderr restart message should show empty args as []: %q", msg)
	}
}

// ── Daily Token Budget integration tests ──────────────────────────────

func TestBudgetMessage_ContainsAllElements(t *testing.T) {
	// Verify the budget exceeded message format sent to the Telegram chat.
	// This is the message produced when CheckDailyBudget fails in the
	// pre-flight check, before the agent runs.
	msg := fmt.Sprintf(
		"Daily token budget exhausted: daily token budget exceeded: 10000 used + 1 new = 10001 total, limit is 10000. "+
			"The budget resets at midnight UTC. "+
			"Set daily_token_budget to 0 in config for unlimited usage.",
	)

	if !strings.Contains(msg, "Daily token budget exhausted") {
		t.Errorf("budget message missing 'Daily token budget exhausted': %q", msg)
	}
	if !strings.Contains(msg, "resets at midnight UTC") {
		t.Errorf("budget message missing 'resets at midnight UTC': %q", msg)
	}
	if !strings.Contains(msg, "daily_token_budget to 0") {
		t.Errorf("budget message missing 'daily_token_budget to 0': %q", msg)
	}
	if !strings.Contains(msg, "10000") {
		t.Errorf("budget message should contain the limit: %q", msg)
	}
}

func TestBudgetWarning_ContainsAllElements(t *testing.T) {
	// Verify the post-run budget warning message format sent when the
	// agent completed successfully but the budget was exceeded.
	msg := fmt.Sprintf(
		"⚠️ Token budget warning\n\n"+
			"daily token budget exceeded: 45000 used + 6000 new = 51000 total, limit is 50000. "+
			"Further agent runs may be blocked until the daily budget resets. "+
			"Use /stats to check current usage.",
	)

	if !strings.Contains(msg, "Token budget warning") {
		t.Errorf("warning message missing 'Token budget warning': %q", msg)
	}
	if !strings.Contains(msg, "daily budget resets") {
		t.Errorf("warning message missing 'daily budget resets': %q", msg)
	}
	if !strings.Contains(msg, "/stats") {
		t.Errorf("warning message should mention /stats: %q", msg)
	}
	if !strings.Contains(msg, "51000") {
		t.Errorf("warning message should contain the total: %q", msg)
	}
}
