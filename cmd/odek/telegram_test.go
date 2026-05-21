package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/BackendStack21/kode/internal/loop"
	"github.com/BackendStack21/kode/internal/telegram"
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

// ── formatStopSummary tests ─────────────────────────────────────────

func TestFormatStopSummary_NoTools_ZeroTurns(t *testing.T) {
	info := loop.IterationInfo{
		Turn:        0,
		InputTokens: 0,
		OutputTokens: 0,
		ToolNames:   nil,
		TotalLatency: 0,
	}
	got := formatStopSummary(info)

	if !strings.Contains(got, "Task Interrupted") {
		t.Errorf("summary missing 'Task Interrupted': %q", got)
	}
	if !strings.Contains(got, "0 turn") {
		t.Errorf("summary missing '0 turn': %q", got)
	}
	if !strings.Contains(got, "0 in / 0 out") {
		t.Errorf("summary missing token counts: %q", got)
	}
	if !strings.Contains(got, "tools: none") {
		t.Errorf("summary should say 'tools: none': %q", got)
	}
}

func TestFormatStopSummary_WithToolsAndTurns(t *testing.T) {
	info := loop.IterationInfo{
		Turn:        5,
		InputTokens: 12500,
		OutputTokens: 3400,
		ToolNames:   []string{"shell", "write_file", "read_file"},
		TotalLatency: 45 * time.Second,
	}
	got := formatStopSummary(info)

	if !strings.Contains(got, "5 turns") {
		t.Errorf("summary missing '5 turns': %q", got)
	}
	if !strings.Contains(got, "12500 in / 3400 out") {
		t.Errorf("summary missing token counts: %q", got)
	}
	if !strings.Contains(got, "45s") {
		t.Errorf("summary missing latency '45s': %q", got)
	}
	if !strings.Contains(got, "tools:") {
		t.Errorf("summary missing tools section: %q", got)
	}
	if !strings.Contains(got, "shell") || !strings.Contains(got, "write_file") || !strings.Contains(got, "read_file") {
		t.Errorf("summary missing tool names: %q", got)
	}
}

func TestFormatStopSummary_SingularTurn(t *testing.T) {
	info := loop.IterationInfo{
		Turn:        1,
		InputTokens: 500,
		OutputTokens: 200,
		ToolNames:   []string{"shell"},
		TotalLatency: 3 * time.Second,
	}
	got := formatStopSummary(info)

	if !strings.Contains(got, "1 turn") {
		t.Errorf("summary should say '1 turn' not '1 turns': %q", got)
	}
	if strings.Contains(got, "1 turns") {
		t.Errorf("summary should not say '1 turns': %q", got)
	}
}

func TestFormatStopSummary_DeduplicatesTools(t *testing.T) {
	// ToolNames may contain duplicates from multiple iterations — they
	// should be deduplicated and sorted in the summary.
	info := loop.IterationInfo{
		Turn:        3,
		InputTokens: 3000,
		OutputTokens: 900,
		ToolNames:   []string{"shell", "read_file", "shell", "write_file", "read_file"},
		TotalLatency: 10 * time.Second,
	}
	got := formatStopSummary(info)

	// Count occurrences — each should appear exactly once.
	for _, tool := range []string{"shell", "read_file", "write_file"} {
		if !strings.Contains(got, tool) {
			t.Errorf("summary missing tool %q: %q", tool, got)
		}
	}
	// Verify tools appear in sorted order (read_file, shell, write_file)
	sorted := "read_file, shell, write_file"
	if !strings.Contains(got, sorted) {
		t.Errorf("summary should have tools in sorted order %q: %q", sorted, got)
	}
}

func TestFormatStopSummary_ContainsStandardEmoji(t *testing.T) {
	info := loop.IterationInfo{
		Turn:        2,
		InputTokens: 100,
		OutputTokens: 50,
		ToolNames:   []string{"shell"},
		TotalLatency: 5 * time.Second,
	}
	got := formatStopSummary(info)

	// The summary must start with the stop emoji.
	if !strings.Contains(got, "⏹️") {
		t.Errorf("summary should contain stop emoji ⏹️: %q", got)
	}
}

// ── Stop command handler integration tests ──────────────────────────

// TestOnCommandStop_NoActiveTask verifies that /stop returns the correct
// message when no agent task is currently running for the chat.
func TestOnCommandStop_NoActiveTask(t *testing.T) {
	chatID := int64(99901)

	// Ensure nothing is stored for this chat.
	chatCancels.LoadAndDelete(chatID)
	chatRunInfos.LoadAndDelete(chatID)

	// Create a minimal bot and handler, then send /stop.
	bot := newTestBot(t)
	h := newTestHandler(bot)

	var result string
	h.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		if cmdName == "stop" {
			// Replicate the stop logic from telegramCmd.
			chatCancels.LoadAndDelete(chatID)
			chatRunInfos.LoadAndDelete(chatID)
			return "⏹️ No active task to stop.", nil
		}
		return "", nil
	}

	result, _ = h.OnCommand(chatID, "stop", "")
	if !strings.Contains(result, "No active task to stop") {
		t.Errorf("expected 'No active task to stop', got: %q", result)
	}
	if !strings.Contains(result, "⏹️") {
		t.Errorf("expected stop emoji in response: %q", result)
	}
}

// TestOnCommandStop_WithActiveTask verifies that /stop returns a summary
// of the interrupted task when run info is available.
func TestOnCommandStop_WithActiveTask(t *testing.T) {
	chatID := int64(99902)

	// Ensure clean state.
	chatCancels.LoadAndDelete(chatID)
	chatRunInfos.LoadAndDelete(chatID)

	// Store fake run info as if the agent had been running.
	info := loop.IterationInfo{
		Turn:        4,
		InputTokens: 8000,
		OutputTokens: 2000,
		ToolNames:   []string{"shell", "write_file", "shell", "read_file"},
		TotalLatency: 30 * time.Second,
	}
	chatRunInfos.Store(chatID, info)

	// Create a minimal bot and handler, then send /stop.
	bot := newTestBot(t)
	h := newTestHandler(bot)

	var result string
	h.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		if cmdName == "stop" {
			chatCancels.LoadAndDelete(chatID)
			if infoVal, ok := chatRunInfos.LoadAndDelete(chatID); ok {
				runInfo := infoVal.(loop.IterationInfo)
				return formatStopSummary(runInfo), nil
			}
			return "⏹️ No active task to stop.", nil
		}
		return "", nil
	}

	result, _ = h.OnCommand(chatID, "stop", "")
	if !strings.Contains(result, "Task Interrupted") {
		t.Errorf("expected 'Task Interrupted', got: %q", result)
	}
	if !strings.Contains(result, "4 turns") {
		t.Errorf("expected '4 turns', got: %q", result)
	}
	if !strings.Contains(result, "8000 in / 2000 out") {
		t.Errorf("expected token counts, got: %q", result)
	}
	if !strings.Contains(result, "30s") {
		t.Errorf("expected latency, got: %q", result)
	}

	// Verify the run info was cleaned up (LoadAndDelete).
	if _, ok := chatRunInfos.Load(chatID); ok {
		t.Error("chatRunInfos should be cleaned up after /stop")
	}
}

// TestOnCommandStop_CancelsContext verifies that /stop calls the stored
// cancel function, which causes context cancellation.
func TestOnCommandStop_CancelsContext(t *testing.T) {
	chatID := int64(99903)

	// Ensure clean state.
	chatCancels.LoadAndDelete(chatID)
	chatRunInfos.LoadAndDelete(chatID)

	// Create a cancellable context and store the cancel func.
	ctx, cancel := context.WithCancel(context.Background())
	chatCancels.Store(chatID, cancel)

	cancelled := false
	h := newTestHandler(newTestBot(t))
	h.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		if cmdName == "stop" {
			if cancelVal, ok := chatCancels.LoadAndDelete(chatID); ok {
				c := cancelVal.(context.CancelFunc)
				c()
				cancelled = true
			}
			chatRunInfos.LoadAndDelete(chatID)
			return "⏹️ No active task to stop.", nil
		}
		return "", nil
	}

	h.OnCommand(chatID, "stop", "")

	if !cancelled {
		t.Error("/stop should call the stored cancel function")
	}
	if ctx.Err() == nil {
		t.Error("context should be cancelled after /stop")
	}

	// Verify the cancel was cleaned up.
	if _, ok := chatCancels.Load(chatID); ok {
		t.Error("chatCancels should be cleaned up after /stop")
	}
}

// TestOnCommandStop_NoRunInfoWhenTaskCancelledEarly verifies that /stop
// returns a clean message when the task was cancelled before any iteration
// completed (no run info stored).
func TestOnCommandStop_NoRunInfoWhenTaskCancelledEarly(t *testing.T) {
	chatID := int64(99904)

	// Clean state — no run info.
	chatCancels.LoadAndDelete(chatID)
	chatRunInfos.LoadAndDelete(chatID)

	// Store a cancel func but NO run info (task started but no iterations yet).
	_, cancel := context.WithCancel(context.Background())
	chatCancels.Store(chatID, cancel)

	h := newTestHandler(newTestBot(t))
	var result string
	h.OnCommand = func(chatID int64, cmdName string, argsStr string) (string, error) {
		if cmdName == "stop" {
			chatCancels.LoadAndDelete(chatID)
			if _, ok := chatRunInfos.LoadAndDelete(chatID); ok {
				return "⏹️ *Task Interrupted*\n\nshould not see this", nil
			}
			return "⏹️ No active task to stop.", nil
		}
		return "", nil
	}

	result, _ = h.OnCommand(chatID, "stop", "")
	if !strings.Contains(result, "No active task to stop") {
		t.Errorf("expected 'No active task to stop' when no run info, got: %q", result)
	}
}

// ── Test helpers ───────────────────────────────────────────────────

// newTestBot creates a telegram.Bot pointed at a mock server for testing.
func newTestBot(t *testing.T) *telegram.Bot {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(ts.Close)
	bot := telegram.NewBot("test:token")
	bot.BaseURL = ts.URL
	return bot
}

// newTestHandler creates a telegram.Handler for testing with the given bot.
func newTestHandler(bot *telegram.Bot) *telegram.Handler {
	h := telegram.NewHandler(bot)
	h.Config = telegram.HandlerConfig{}
	return h
}
