package session

import "testing"

func TestRED_MergeCheckpointKeepsLaterSupersededFlag(t *testing.T) {
	durable := []Message{
		{ID: "a", Role: "system", Content: "s"},
		{ID: "b", Role: "user", Content: "q"},
		{ID: "c", Role: "assistant", Content: "draft"},
	}
	snap := CloneMessages(durable)
	snap[2].Superseded = true
	snap[2].SupersededReason = "verify"
	got := MergeCheckpoint(durable, snap)
	if len(got) != 3 || !got[2].Superseded || got[2].SupersededReason != "verify" {
		t.Fatalf("superseded flag lost on merge: %+v", got[2])
	}
}
