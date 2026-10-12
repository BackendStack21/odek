package session

import (
	"encoding/json"
	"strings"
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

// A signed block whose text redaction altered can never be replayed; the
// whole signed reasoning is dropped on save, while untouched blocks persist.
func TestRED_RuntimeSaveDropsSignedThinkingWhenRedactionChangesIt(t *testing.T) {
	store := newTestStore(t)
	secret := "sk-live-1234567890abcdef1234567890abcdef"
	msgs := []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "a", ThinkingProvider: "anthropic",
			ThinkingBlocks: []ThinkingBlock{{Text: "key " + secret, Signature: "SIG"}}},
		{Role: "assistant", Content: "b", ThinkingProvider: "anthropic", ThinkingSignature: "SIG2",
			ReasoningContent: "key " + secret},
		{Role: "assistant", Content: "c", ThinkingProvider: "anthropic",
			ThinkingBlocks: []ThinkingBlock{{Text: "clean", Signature: "SIG3"}}},
	}
	sess, err := store.Create(msgs, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 3} {
		m := loaded.Messages[i]
		if len(m.ThinkingBlocks) != 0 || m.ThinkingSignature != "" || m.ThinkingProvider != "" || strings.Contains(m.ReasoningContent, secret) {
			t.Fatalf("message %d kept unreplayable signed reasoning: %+v", i, m)
		}
	}
	if m := loaded.Messages[4]; len(m.ThinkingBlocks) != 1 || m.ThinkingBlocks[0].Signature != "SIG3" || m.ThinkingProvider != "anthropic" {
		t.Fatalf("clean block dropped: %+v", m)
	}
}
