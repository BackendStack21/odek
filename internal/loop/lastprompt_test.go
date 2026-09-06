package loop

// Tests for the parent-only context window contract (wire v3):
//   - LastPromptTokens reports the provider-normalized full prompt size of
//     the LAST main-path LLM call (CallResult cache semantics are EXCLUSIVE:
//     InputTokens + CacheReadTokens + CacheCreationTokens), never a
//     cumulative.
//   - External (child) usage charged via ChargeExternalUsage must feed
//     billing totals (TotalInputTokens) but NEVER the window — the ctx
//     gauge must stay on the parent's conversation while swarms run.

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestPromptWindowTokens_Normalization(t *testing.T) {
	// CallResult cache semantics are exclusive (go-llm-sdk adapters subtract
	// cached tokens from InputTokens into CacheRead/CacheCreation), so the
	// full window is the plain sum. If an adapter ever reported inclusive
	// prompt tokens, this test would pin the arithmetic it must preserve.
	cases := []struct {
		name string
		res  llmclient.CallResult
		want int
	}{
		{
			name: "cache components excluded from input — sum them",
			res:  llmclient.CallResult{InputTokens: 80, CacheReadTokens: 300, CacheCreationTokens: 20},
			want: 400,
		},
		{
			name: "no cache reported — input is the window",
			res:  llmclient.CallResult{InputTokens: 380},
			want: 380,
		},
		{
			name: "no usage reported",
			res:  llmclient.CallResult{},
			want: 0,
		},
	}
	for _, tc := range cases {
		if got := promptWindowTokens(&tc.res); got != tc.want {
			t.Errorf("%s: promptWindowTokens = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestEngine_LastPromptTokens_ParentOnly(t *testing.T) {
	server := scriptedServer(
		budgetToolCallResponse("call_1", "count", 100, 10),
		budgetFinalResponse("all done", 200, 5),
	)
	defer server.Close()

	// maxContext = 0 (unknown model): accessors and frames must tolerate it.
	engine := New(testChatClient(t, server.URL),
		tool.NewRegistry([]tool.Tool{&countingTool{}}), 10, "", nil, 0)
	if got := engine.MaxContext(); got != 0 {
		t.Fatalf("MaxContext = %d, want 0 (unknown model)", got)
	}

	var lastWindow int
	engine.SetIterationCallback(func(info IterationInfo) {
		lastWindow = info.WindowTokens
	})

	if _, err := engine.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Window = the LAST main-path call's prompt (200), not the cumulative (300).
	if got := engine.LastPromptTokens(); got != 200 {
		t.Errorf("LastPromptTokens = %d, want 200 (last main-path call's prompt, not cumulative 300)", got)
	}
	if lastWindow != 200 {
		t.Errorf("IterationInfo.WindowTokens = %d, want 200", lastWindow)
	}

	// Charge-back isolation: child spend feeds billing totals only.
	engine.ChargeExternalUsage(9000)
	if got := engine.LastPromptTokens(); got != 200 {
		t.Errorf("LastPromptTokens after ChargeExternalUsage = %d, want 200 (window must ignore child spend)", got)
	}
	if engine.TotalInputTokens != 9300 {
		t.Errorf("TotalInputTokens after charge-back = %d, want 9300 (billing keeps child spend)", engine.TotalInputTokens)
	}
}
