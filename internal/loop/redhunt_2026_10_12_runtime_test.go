package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func TestRED_RuntimeStripOldReasoningClearsThinkingBlocks(t *testing.T) {
	msgs := []session.Message{
		{Role: "assistant", ThinkingProvider: "anthropic", ThinkingBlocks: []session.ThinkingBlock{{Redacted: "x"}}},
		{Role: "assistant", ThinkingProvider: "anthropic", ThinkingBlocks: []session.ThinkingBlock{{Redacted: "y"}}},
	}
	stripOldReasoning(msgs, 1)
	if len(msgs[0].ThinkingBlocks) != 0 || msgs[0].ThinkingProvider != "" {
		t.Fatalf("old blocks kept: %+v", msgs[0])
	}
	if len(msgs[1].ThinkingBlocks) != 1 {
		t.Fatalf("newest blocks dropped: %+v", msgs[1])
	}
}
