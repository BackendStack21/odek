package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── CancelTask: user-initiated sub-agent stops ──────────────────────────
//
// The Web UI exposes a per-card stop button backed by the WS
// subagent_cancel message; the server resolves the task_id through the
// process-global cancel registry populated by runTask. These tests pin the
// contract: cancel kills the child promptly, the parent sees a
// "cancelled" result (not a misleading timeout), the terminal state is
// surfaced via OnSubagentDone (the child cannot announce its own death),
// and the registry entry does not leak after exit.

// hangingScript writes a mock sub-agent that emits one progress line and
// then sleeps far beyond the test budget.
func hangingScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-hanging-subagent.sh")
	body := "#!/bin/sh\n" +
		"echo '{\"type\":\"tool_call\",\"name\":\"shell\",\"data\":\"x\"}'\n" +
		"sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	return script
}

func TestDelegateTasksTool_CancelTask_KillsRunningSubagent(t *testing.T) {
	tool := &delegateTasksTool{
		maxConcurrency: 1,
		odekPath:       hangingScript(t),
		timeout:        30 * time.Second,
	}

	var mu sync.Mutex
	var doneStatus string
	var doneTaskID string
	tool.OnSubagentDone = func(taskIdx int, taskID, status string) {
		mu.Lock()
		defer mu.Unlock()
		doneTaskID, doneStatus = taskID, status
	}

	var logTaskID string
	logged := make(chan struct{}, 1)
	tool.OnSubagentLog = func(taskIdx int, taskID string, line string) {
		if logTaskID == "" {
			logTaskID = taskID
			logged <- struct{}{}
		}
	}

	resultCh := make(chan string, 1)
	go func() { resultCh <- tool.runTask(0, "long task", "", "", "", "", "") }()

	select {
	case <-logged:
	case <-time.After(5 * time.Second):
		t.Fatal("sub-agent never emitted its first progress line")
	}

	if logTaskID == "" {
		t.Fatal("OnSubagentLog did not carry a task id")
	}
	if !tool.CancelTask(logTaskID) {
		t.Fatalf("CancelTask(%q) = false, want true for a live task", logTaskID)
	}

	select {
	case result := <-resultCh:
		if !strings.Contains(result, `"cancelled"`) {
			t.Errorf("runTask result after cancel should carry status cancelled, got: %s", result)
		}
		if strings.Contains(result, "timeout") {
			t.Errorf("user cancel must not be framed as timeout, got: %s", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runTask did not return promptly after CancelTask")
	}

	mu.Lock()
	defer mu.Unlock()
	if doneTaskID != logTaskID || doneStatus != "cancelled" {
		t.Errorf("OnSubagentDone = (%q, %q), want (%q, \"cancelled\")", doneTaskID, doneStatus, logTaskID)
	}

	// Registry entry must be gone after the task exits.
	if tool.CancelTask(logTaskID) {
		t.Error("CancelTask after task exit = true, want false (entry must be unregistered)")
	}
	if cancelSubagentTask(logTaskID) {
		t.Error("global registry still holds the finished task's cancel func")
	}
}

func TestDelegateTasksTool_CancelTask_UnknownTaskID(t *testing.T) {
	tool := &delegateTasksTool{}
	if tool.CancelTask("") {
		t.Error("CancelTask(\"\") = true, want false")
	}
	if tool.CancelTask("no-such-task") {
		t.Error("CancelTask(unknown) = true, want false")
	}
}

func TestCancelSubagentTask_GlobalRegistry(t *testing.T) {
	cancelled := make(chan struct{}, 1)

	unregister := registerSubagentCancel("task-abc", func() {
		cancelled <- struct{}{}
	})

	if !cancelSubagentTask("task-abc") {
		t.Fatal("cancelSubagentTask(live id) = false, want true")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("registered cancel func was never invoked")
	}

	// Unknown ids report false without side effects.
	if cancelSubagentTask("") || cancelSubagentTask("nope") {
		t.Error("cancelSubagentTask(unknown) = true, want false")
	}

	unregister()
	if cancelSubagentTask("task-abc") {
		t.Error("cancelSubagentTask after unregister = true, want false")
	}
}

// ── Exit status framing: cancelled vs timeout ───────────────────────────

func TestSubagentExitStatus_CancelledVsTimeout(t *testing.T) {
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if got := subagentExitStatus(nil, nil, cctx, nil); got != "cancelled" {
		t.Errorf("subagentExitStatus(canceled ctx) = %q, want \"cancelled\"", got)
	}

	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer dcancel()
	<-dctx.Done()
	if got := subagentExitStatus(nil, nil, dctx, nil); got != "timeout" {
		t.Errorf("subagentExitStatus(deadline ctx) = %q, want \"timeout\"", got)
	}

	// A child that reported its own status still wins.
	if got := subagentExitStatus(map[string]any{"status": "success"}, nil, cctx, nil); got != "success" {
		t.Errorf("subagentExitStatus(parsed result) = %q, want \"success\"", got)
	}
}

// ── OnSubagentDone: only for children that cannot report ────────────────

func TestDelegateTasksTool_OnSubagentDone_NotFiredWhenChildReports(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-ok-subagent.sh")
	body := "#!/bin/sh\n" +
		"echo '{\"type\":\"subagent_started\",\"task_id\":\"child-tid\",\"status\":\"running\"}'\n" +
		"echo '{\"status\":\"success\",\"summary\":\"done\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	tool := &delegateTasksTool{maxConcurrency: 1, odekPath: script, timeout: 10 * time.Second}
	var fired bool
	var mu sync.Mutex
	tool.OnSubagentDone = func(taskIdx int, taskID, status string) {
		mu.Lock()
		defer mu.Unlock()
		fired = true
	}

	result := tool.runTask(0, "ok task", "", "", "", "", "")
	if !strings.Contains(result, "done") {
		t.Errorf("expected successful result, got: %s", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if fired {
		t.Error("OnSubagentDone fired although the child reported its own finish")
	}
}

// ── Parent-side done relay: registry + WS state ─────────────────────────

func TestSubagentDoneRelay_UpdatesRegistryAndEmitsState(t *testing.T) {
	taskID := newTaskID()
	subagentRegistryRecord(&subagentEntry{
		TaskID: taskID, RunKey: "conn-1", Goal: "g", Phase: "started", Status: "running",
	})

	var sent []map[string]any
	relay := newSubagentDoneRelay(func(v any) error {
		sent = append(sent, v.(map[string]any))
		return nil
	}, "conn-1")

	relay(2, taskID, "cancelled")

	snap := subagentRegistrySnapshot("")
	var entry *subagentEntry
	for i := range snap {
		if snap[i].TaskID == taskID {
			entry = &snap[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("registry lost the task entry after done relay")
	}
	if entry.Phase != "finished" || entry.Status != "cancelled" {
		t.Errorf("registry entry = (%s, %s), want (finished, cancelled)", entry.Phase, entry.Status)
	}
	if !entry.FinishedAt.After(time.Now().Add(-time.Minute)) {
		t.Error("FinishedAt not stamped by the done relay")
	}

	if len(sent) != 1 {
		t.Fatalf("relay sent %d WS messages, want 1", len(sent))
	}
	msg := sent[0]
	if msg["type"] != "subagent_state" || msg["task_id"] != taskID ||
		msg["task_idx"] != 2 || msg["phase"] != "finished" || msg["status"] != "cancelled" {
		t.Errorf("subagent_state message = %v, want finished/cancelled for %s", msg, taskID)
	}

	// A user cancel is neither success nor failure for lifetime counters.
	beforeCompleted := subagentStats.completed.Load()
	beforeFailed := subagentStats.failed.Load()
	relay(2, newTaskID(), "cancelled")
	if subagentStats.completed.Load() != beforeCompleted || subagentStats.failed.Load() != beforeFailed {
		t.Error("cancelled tasks must not bump completed/failed counters")
	}

	// Non-reported failures (timeout, crash) still count as failed.
	beforeFailed = subagentStats.failed.Load()
	relay(3, newTaskID(), "timeout")
	if subagentStats.failed.Load() != beforeFailed+1 {
		t.Error("timeout done relay did not count the task as failed")
	}
}

func TestSubagentDoneRelay_CreatesEntryWhenChildNeverStarted(t *testing.T) {
	// A task cancelled between registration and spawn has no registry
	// entry; the relay must still record a terminal entry so a reload
	// mid-batch sees the final state.
	taskID := newTaskID()
	relay := newSubagentDoneRelay(func(v any) error { return nil }, "conn-2")
	relay(0, taskID, "cancelled")

	var entry *subagentEntry
	snap := subagentRegistrySnapshot("")
	for i := range snap {
		if snap[i].TaskID == taskID {
			entry = &snap[i]
			break
		}
	}
	if entry == nil || entry.Phase != "finished" {
		t.Errorf("relay did not create a terminal entry, got: %+v", entry)
	}
}
