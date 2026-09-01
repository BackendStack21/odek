package main

// Bug-sweep batch 1 (fix/bug-hunt-b1) — B4 regression test.
//
// RED-first: the registry ring evicted entries[0] regardless of phase, so a
// still-running sub-agent could be evicted under ring pressure; its next
// progress record then auto-created a hollow entry without RunKey, making
// the task invisible to run-filtered snapshots (the reload-restore path).

import (
	"fmt"
	"testing"
	"time"
)

func TestSubagentRegistry_EvictionKeepsLiveEntriesVisible(t *testing.T) {
	// Snapshot and restore the process-global registry around the test.
	subagentReg.mu.Lock()
	savedEntries, savedByID := subagentReg.entries, subagentReg.byID
	subagentReg.mu.Unlock()
	defer func() {
		subagentReg.mu.Lock()
		subagentReg.entries, subagentReg.byID = savedEntries, savedByID
		subagentReg.mu.Unlock()
	}()

	live := newTaskID()
	subagentRegistryRecord(&subagentEntry{
		TaskID: live, RunKey: "r1", Goal: "important work",
		Phase: "started", Status: "running",
	})
	// Overflow the ring with finished entries (evictable by preference).
	for i := 0; i < maxSubagentRegistryEntries+10; i++ {
		e := &subagentEntry{
			TaskID: fmt.Sprintf("filler-%d-%d", i, time.Now().UnixNano()),
			RunKey: "r1",
			Phase:  "finished",
			Status: "success",
		}
		e.FinishedAt = time.Now()
		subagentRegistryRecord(e)
	}

	entries := subagentRegistrySnapshot("r1")
	var found *subagentEntry
	for i := range entries {
		if entries[i].TaskID == live {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("live entry evicted while finished entries were evictable (ring=%d)", maxSubagentRegistryEntries)
	}
	if found.Goal != "important work" || found.RunKey != "r1" {
		t.Fatalf("live entry decapitated: goal=%q runKey=%q", found.Goal, found.RunKey)
	}

	// A progress record for the (surviving) task must keep it visible in
	// the run-filtered snapshot — auto-recreate must not strip RunKey.
	subagentRegistryUpdate(live, "r1", func(e *subagentEntry) { e.Step = 2 })
	entries = subagentRegistrySnapshot("r1")
	found = nil
	for i := range entries {
		if entries[i].TaskID == live {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("progress update recreated the entry without RunKey; task invisible in run-filtered snapshot")
	}
	if found.Step != 2 {
		t.Fatalf("progress update not applied: step = %d, want 2", found.Step)
	}
}
