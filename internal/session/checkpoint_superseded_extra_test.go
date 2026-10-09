package session

import "testing"

func TestMergeCheckpointSupersededDoesNotRewriteOtherFields(t *testing.T) {
	durable := []Message{
		{ID: "a", Role: "assistant", Content: "draft", Superseded: true, SupersededReason: "first"},
		{ID: "b", Role: "assistant", Content: "final"},
	}
	snap := CloneMessages(durable)
	snap[0].Content = "mutated"
	snap[0].SupersededReason = "second"
	snap[1].Superseded = true
	snap[1].SupersededReason = "verify"
	got := MergeCheckpoint(durable, snap)
	if got[0].Content != "draft" || got[0].SupersededReason != "first" {
		t.Fatalf("already-superseded record rewritten: %+v", got[0])
	}
	if !got[1].Superseded || got[1].SupersededReason != "verify" || got[1].Content != "final" {
		t.Fatalf("flag not carried: %+v", got[1])
	}
	if durable[1].Superseded {
		t.Fatal("input slice aliased")
	}
}
