package session

import (
	"encoding/json"
	"testing"
)

// Signed thinking blocks survive a JSON round trip and are deep-copied by
// CloneMessages.
func TestRED_RuntimeThinkingBlocksPersistAndClone(t *testing.T) {
	m := Message{Role: "assistant", ThinkingProvider: "anthropic", ThinkingBlocks: []ThinkingBlock{{Text: "a", Signature: "S1"}, {Redacted: "OPAQUE"}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ThinkingProvider != "anthropic" || len(back.ThinkingBlocks) != 2 || back.ThinkingBlocks[1].Redacted != "OPAQUE" || back.ThinkingBlocks[0].Signature != "S1" {
		t.Fatalf("round trip lost blocks: %+v", back)
	}
	c := CloneMessages([]Message{m})
	c[0].ThinkingBlocks[0].Text = "changed"
	if m.ThinkingBlocks[0].Text != "a" {
		t.Fatal("CloneMessages shares the ThinkingBlocks slice")
	}
}
