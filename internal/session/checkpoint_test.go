package session

import "testing"

func TestCheckpointKeepsCompletedRecordsWhenContextShrinks(t *testing.T) {
	original := []Message{{Role: "user", Content: "update", TurnID: "turn"}, {Role: "assistant", Content: "old turn"}, {Role: "tool", Content: "mutation completed", TurnID: "turn"}}
	EnsureMessageIDs(original)
	projection := CloneMessages([]Message{original[0], original[2]})
	projection[1].Content = "[trimmed]"
	projection = append(projection, Message{Role: "assistant", Content: "done", TurnID: "turn"})
	EnsureMessageIDs(projection)
	checkpoint := MergeCheckpoint(original, projection)
	if len(checkpoint) != 4 || checkpoint[2].Content != "mutation completed" || checkpoint[3].Content != "done" {
		t.Fatalf("lost durable progress: %+v", checkpoint)
	}
	if again := MergeCheckpoint(checkpoint, projection); len(again) != 4 {
		t.Fatalf("repeated checkpoint duplicated records: %+v", again)
	}
	if turn := TurnMessages(checkpoint, "turn"); len(turn) != 3 || turn[2].Content != "done" {
		t.Fatalf("unstable turn extraction: %+v", turn)
	}
	checkpoint[0].Content = "changed"
	if original[0].Content != "update" {
		t.Fatal("checkpoint aliases original")
	}
}
