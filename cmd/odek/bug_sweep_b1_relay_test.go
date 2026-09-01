package main

// Bug-sweep batch 1 (fix/bug-hunt-b1) — B5 regression test.
//
// RED-first: when a child emitted subagent_finished(status=success) but its
// framed result line failed to parse, the parent done relay unconditionally
// overwrote the terminal state to "failed" and bumped the failed counter —
// the card showed failed-with-cost and both lifetime counters counted the
// same task.

import "testing"

func TestSubagentDoneRelay_DoesNotClobberChildReportedSuccess(t *testing.T) {
	taskID := newTaskID()
	// Child reported success through the telemetry stream:
	subagentRegistryRecord(&subagentEntry{
		TaskID: taskID, RunKey: "conn-b1", Phase: "finished", Status: "success",
	})
	beforeFailed := subagentStats.failed.Load()

	// Framed result unparseable → parent done-relay reports "failed":
	relay := newSubagentDoneRelay(func(v any) error { return nil }, "conn-b1")
	relay(0, taskID, "failed")

	entries := subagentRegistrySnapshot("")
	var e *subagentEntry
	for i := range entries {
		if entries[i].TaskID == taskID {
			e = &entries[i]
		}
	}
	if e == nil {
		t.Fatal("registry entry missing")
	}
	if e.Status != "success" {
		t.Fatalf("child-reported success clobbered to %q by the done relay", e.Status)
	}
	if after := subagentStats.failed.Load(); after != beforeFailed {
		t.Fatalf("failed counter bumped for an already-successful task: %d -> %d", beforeFailed, after)
	}
}
