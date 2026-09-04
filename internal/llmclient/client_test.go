package llmclient

import (
	"encoding/json"
	"testing"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/session"
)

func TestSDKTemperature(t *testing.T) {
	if got := sdkTemperature(0); got != -1 {
		t.Fatalf("odek 0 → %v, want -1", got)
	}
	if got := sdkTemperature(-1); got != 0 {
		t.Fatalf("odek -1 → %v, want 0", got)
	}
	if got := sdkTemperature(0.7); got != 0.7 {
		t.Fatalf("odek 0.7 → %v", got)
	}
}

func TestInferProviderAndCanonicalBase(t *testing.T) {
	if got := InferProvider("https://api.anthropic.com/v1"); got != "anthropic" {
		t.Fatalf("infer anthropic: %q", got)
	}
	if got := CanonicalBaseURL("anthropic", "https://api.anthropic.com/v1"); got != "" {
		t.Fatalf("official anthropic /v1 must not be copied, got %q", got)
	}
	if got := CanonicalBaseURL("anthropic", "https://proxy.example/anthropic"); got != "https://proxy.example/anthropic" {
		t.Fatalf("custom anthropic host: %q", got)
	}
	if got := InferProvider("https://api.deepseek.com/v1"); got != "deepseek" {
		t.Fatalf("infer deepseek: %q", got)
	}
	if got := InferProvider("http://localhost:11434/v1"); got != "" {
		t.Fatalf("unknown host should be empty, got %q", got)
	}
}

func TestToSDKMessages_ToolNameFromV1Name(t *testing.T) {
	_, msgs := toSDKMessages([]session.Message{
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Type: "function"}}},
		{Role: "tool", Name: "shell", ToolCallID: "c1", Content: "ok"},
	}, false)
	if len(msgs) != 2 {
		t.Fatalf("len=%d", len(msgs))
	}
	if msgs[1].ToolName != "shell" || msgs[1].Role != sdk.RoleTool {
		t.Fatalf("tool msg = %+v", msgs[1])
	}
}

func TestToSDKMessages_DropsUnknownRoleWithToolGroup(t *testing.T) {
	_, msgs := toSDKMessages([]session.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "call", ToolCalls: []session.ToolCall{{ID: "c1", Type: "function"}}},
		{Role: "garbage", Content: "bad"},
		{Role: "tool", Name: "shell", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "next"},
	}, false)
	for _, m := range msgs {
		if session.UnknownRole(string(m.Role)) {
			t.Fatalf("unknown role leaked: %q", m.Role)
		}
	}
	// The assistant+tool group containing the garbage row must be dropped together.
	if len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Content != "next" {
		b, _ := json.Marshal(msgs)
		t.Fatalf("expected only the two user turns, got %s", b)
	}
}

func TestLastResortContext(t *testing.T) {
	if LastResortContext("deepseek-v4-flash") != 131_072 {
		t.Fatal("flash")
	}
	if LastResortContext("deepseek-v4-pro") != 1_000_000 {
		t.Fatal("pro")
	}
	if LastResortContext("gpt-4o") != 0 {
		t.Fatal("unknown must be 0")
	}
}

func TestMapResult_FlattensToolCalls(t *testing.T) {
	res := mapResult(&sdk.ChatResult{
		Content:   "x",
		ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "shell", Arguments: `{}`}},
		Usage:     sdk.Usage{PromptTokens: 10, CompletionTokens: 2, CacheReadTokens: 3, CacheReported: true},
	})
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "shell" {
		t.Fatalf("toolcalls = %+v", res.ToolCalls)
	}
	if res.InputTokens != 10 || res.CacheReadTokens != 3 || !res.CacheReported {
		t.Fatalf("usage = %+v", res)
	}
}
