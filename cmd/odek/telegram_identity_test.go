package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

// TestSeedSystemMessage locks in the fix for the bug where Telegram chats
// reached the model with no system prompt (RunWithMessages does not inject it),
// causing the agent to answer as the provider's base identity ("I am Claude")
// instead of from IDENTITY.md / the default system prompt.
func TestSeedSystemMessage(t *testing.T) {
	const sys = "You are Molty — AI Chief of Staff."

	t.Run("new empty session prepends system", func(t *testing.T) {
		got := seedSystemMessage(nil, sys)
		if len(got) != 1 || got[0].Role != "system" || got[0].Content != sys {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("user-first history prepends system and keeps the user message", func(t *testing.T) {
		got := seedSystemMessage([]llm.Message{{Role: "user", Content: "hi"}}, sys)
		if len(got) != 2 {
			t.Fatalf("want 2 messages, got %d: %+v", len(got), got)
		}
		if got[0].Role != "system" || got[0].Content != sys {
			t.Errorf("messages[0] = %+v, want system %q", got[0], sys)
		}
		if got[1].Role != "user" || got[1].Content != "hi" {
			t.Errorf("messages[1] = %+v, want user 'hi'", got[1])
		}
	})

	t.Run("resumed history refreshes stale system without duplicating", func(t *testing.T) {
		got := seedSystemMessage([]llm.Message{
			{Role: "system", Content: "OLD PROMPT"},
			{Role: "user", Content: "hi"},
		}, sys)
		if len(got) != 2 {
			t.Fatalf("want 2 messages (no duplicate system), got %d: %+v", len(got), got)
		}
		if got[0].Role != "system" || got[0].Content != sys {
			t.Errorf("messages[0] = %+v, want refreshed system %q", got[0], sys)
		}
	})
}
