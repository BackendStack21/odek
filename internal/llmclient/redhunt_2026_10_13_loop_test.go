package llmclient

import (
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"
	"github.com/BackendStack21/odek/internal/session"
)

func TestToSDKMessagesTrailingSystemBecomesUserTurn(t *testing.T) {
	in := []session.Message{
		{Role: "system", Content: "base"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "draft"},
		{Role: "system", Content: "fix it"},
	}
	sys, msgs := toSDKMessagesFor(in, false, true, "")
	if len(sys) != 1 || len(msgs) != 3 || msgs[2].Role != sdk.RoleUser || msgs[2].Content != "fix it" {
		t.Fatalf("sys=%v msgs=%v", sys, msgs)
	}
	_, msgs = toSDKMessagesFor(in, false, false, "")
	if len(msgs) != 2 {
		t.Fatalf("non-anthropic conversion must keep hoisting: %v", msgs)
	}
}

func TestToSDKMessagesCapsSystemCacheBreakpoints(t *testing.T) {
	cc := &session.CacheControl{Type: "ephemeral"}
	in := []session.Message{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "m1", CacheControl: cc},
		{Role: "system", Content: "m2", CacheControl: cc},
		{Role: "user", Content: "q"},
	}
	sys, _ := toSDKMessagesFor(in, true, true, "")
	n := 0
	for _, b := range sys {
		if b.Cache {
			n++
		}
	}
	if n != 2 || !sys[0].Cache || !sys[2].Cache {
		t.Fatalf("breakpoints: %+v", sys)
	}
}
