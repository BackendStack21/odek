package llmclient

import (
	"context"
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

func TestToSDKMessages_AnthropicCacheMarkers(t *testing.T) {
	sys, msgs := toSDKMessages([]session.Message{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "memory"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "yo"},
		{Role: "user", Content: "again"},
	}, true)
	if len(sys) != 2 || !sys[0].Cache || sys[1].Cache {
		t.Fatalf("system cache = %+v", sys)
	}
	if len(msgs) != 3 || !msgs[0].Cache || msgs[1].Cache || msgs[2].Cache {
		t.Fatalf("message cache = %+v", msgs)
	}
}

func TestToSDKMessages_NoCacheWhenDisabled(t *testing.T) {
	sys, msgs := toSDKMessages([]session.Message{
		{Role: "system", Content: "base"},
		{Role: "user", Content: "hi"},
	}, false)
	if sys[0].Cache || msgs[0].Cache {
		t.Fatal("cache markers must be off when cacheAnthropic is false")
	}
}

func TestDiscoverContext_NilOrEmpty(t *testing.T) {
	if DiscoverContext(context.Background(), nil, "m") != 0 {
		t.Fatal("nil provider")
	}
	if DiscoverContext(context.Background(), nil, "") != 0 {
		t.Fatal("empty model")
	}
}

func TestDiscoverContext_ListModelsFailure(t *testing.T) {
	c, err := Dial("legacy", "llama3", "k", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := DiscoverContext(context.Background(), c.Provider, "llama3"); got != 0 {
		t.Fatalf("failed ListModels must return 0, got %d", got)
	}
}

func TestClient_IsAnthropicFormatNotURL(t *testing.T) {
	ant, err := Dial("anthropic", "claude-sonnet-4-5", "sk-test", "")
	if err != nil {
		t.Fatalf("anthropic Dial: %v", err)
	}
	if !ant.IsAnthropic() {
		t.Fatal("anthropic provider must report FormatAnthropic")
	}
	ds, err := Dial("deepseek", "deepseek-v4-flash", "sk-test", "")
	if err != nil {
		t.Fatalf("deepseek Dial: %v", err)
	}
	if ds.IsAnthropic() {
		t.Fatal("deepseek must not report Anthropic format")
	}
	ds.PromptCache = true
	if ds.PromptCache && ds.IsAnthropic() {
		t.Fatal("cache gate must stay closed for OpenAI-format providers")
	}
}

func TestDial_LegacyUnknownHost(t *testing.T) {
	c, err := Dial("", "llama3", "local", "http://127.0.0.1:9/v1")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if c.ProviderID() != "legacy" {
		t.Errorf("ProviderID = %q, want legacy", c.ProviderID())
	}
	if c.Format() != FormatOpenAI {
		t.Errorf("Format = %q, want openai", c.Format())
	}
}

func TestToolsFromSchema_NilAndMap(t *testing.T) {
	def, err := ToolsFromSchema("echo", "desc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "echo" || string(def.Parameters) != `{"type":"object","properties":{}}` {
		t.Fatalf("nil schema = %+v", def)
	}
	def, err = ToolsFromSchema("echo", "desc", map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(def.Parameters) {
		t.Fatalf("map schema not JSON: %s", def.Parameters)
	}
}

func TestCanonicalBaseURL_GeminiOfficialStripped(t *testing.T) {
	if got := CanonicalBaseURL("gemini", "https://generativelanguage.googleapis.com/v1beta"); got != "" {
		t.Fatalf("official gemini /v1beta must not be copied, got %q", got)
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
