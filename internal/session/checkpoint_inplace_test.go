package session

import "testing"

func TestMergeCheckpointInPlaceKeepsIndexCurrent(t *testing.T) {
	durable := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}, {Role: "assistant", Content: "a"}}
	EnsureMessageIDs(durable)
	idx := NewCheckpointIndex(durable)

	// A dynamic system row lands before the next retained record and every
	// later position is re-indexed.
	snap := CloneMessages(durable)
	digest := Message{Role: "system", Content: "digest"}
	snap = append([]Message{snap[0], digest}, snap[1:]...)
	EnsureMessageIDs(snap)
	durable = MergeCheckpointInPlace(durable, idx, snap)
	if len(durable) != 4 || durable[1].Content != "digest" || durable[2].Content != "task" {
		t.Fatalf("system row not inserted before its successor: %+v", durable)
	}
	for i, m := range durable {
		if idx[m.ID] != i {
			t.Fatalf("index stale for %q: %d, want %d", m.ID, idx[m.ID], i)
		}
	}

	// Refresh in place, supersede later, append new, all through the index.
	snap[1].Content = "digest v2"
	snap[3].Superseded = true
	snap[3].SupersededReason = "retry"
	snap = append(snap, Message{Role: "assistant", Content: "b", ToolCalls: []ToolCall{{ID: "t"}}})
	EnsureMessageIDs(snap)
	durable = MergeCheckpointInPlace(durable, idx, snap)
	if len(durable) != 5 || durable[1].Content != "digest v2" || !durable[3].Superseded || durable[4].Content != "b" {
		t.Fatalf("second merge wrong: %+v", durable)
	}
	snap[4].ToolCalls[0].ID = "changed"
	if durable[4].ToolCalls[0].ID != "t" {
		t.Fatal("adopted record aliases the snapshot")
	}
	if idx[durable[4].ID] != 4 {
		t.Fatalf("appended record not indexed: %v", idx)
	}
}

func TestRED_Session_MergeCheckpointInPlaceNoPerRecordWork(t *testing.T) {
	durable := make([]Message, 1000)
	for i := range durable {
		durable[i] = Message{Role: "assistant", Content: "x"}
	}
	EnsureMessageIDs(durable)
	idx := NewCheckpointIndex(durable)
	snap := CloneMessages(durable[900:])
	allocs := testing.AllocsPerRun(20, func() { durable = MergeCheckpointInPlace(durable, idx, snap) })
	if allocs > 2 {
		t.Fatalf("unchanged merge made %.0f allocations; want none per durable record", allocs)
	}
}

func BenchmarkMergeCheckpointInPlace(b *testing.B) {
	durable := make([]Message, 1000)
	for i := range durable {
		durable[i] = Message{Role: "assistant", Content: "x"}
	}
	EnsureMessageIDs(durable)
	idx := NewCheckpointIndex(durable)
	snap := CloneMessages(durable[900:])
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		durable = MergeCheckpointInPlace(durable, idx, snap)
	}
}
