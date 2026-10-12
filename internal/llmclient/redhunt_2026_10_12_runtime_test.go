package llmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// runtimeHuntAnthropicServer answers the first /v1/messages call with the
// given content blocks (a tool_use turn) and records every request body.
func runtimeHuntAnthropicServer(t *testing.T, firstContent string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","content":`+firstContent+`,"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
			return
		}
		io.WriteString(w, `{"id":"m2","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

// runtimeHuntReplay runs a tool loop turn the way the engine does: call,
// persist the assistant turn from the CallResult DTO, append the tool
// result, and call again. It returns the replayed assistant message from
// the second request body.
func runtimeHuntReplay(t *testing.T, firstContent string) map[string]any {
	t.Helper()
	srv, bodies := runtimeHuntAnthropicServer(t, firstContent)
	c, err := Dial("anthropic", "claude-sonnet-4-5", "sk-test", srv.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	c.Thinking = "high"
	msgs := []session.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "go"}}
	res, err := c.Call(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(res.ToolCalls))
	}
	msgs = append(msgs, session.Message{
		Role:              "assistant",
		Content:           res.Content,
		ReasoningContent:  res.ReasoningContent,
		ThinkingSignature: res.ThinkingSignature,
		ThinkingBlocks:    res.ThinkingBlocks,
		ThinkingProvider:  res.ThinkingProvider,
		ToolCalls:         res.ToolCalls,
	}, session.Message{Role: "tool", Name: "f", ToolCallID: res.ToolCalls[0].ID, Content: "42"})
	if _, err := c.Call(context.Background(), msgs, nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	all := bodies()
	if len(all) != 2 {
		t.Fatalf("requests = %d", len(all))
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(all[1]), &req); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	for _, m := range req.Messages {
		if m["role"] == "assistant" {
			return m
		}
	}
	t.Fatalf("no assistant message replayed: %s", all[1])
	return nil
}

// Anthropic requires every thinking / redacted_thinking block of the
// previous assistant turn to be replayed verbatim in a tool loop. A
// redacted_thinking block (encrypted reasoning) is returned by the SDK in
// ChatResult.ThinkingBlocks, but mapResult/CallResult never carry it, so the
// replayed tool_use turn has no thinking block at all and Anthropic rejects
// the request (thinking enabled → "Expected thinking or redacted_thinking").
func TestRED_AnthropicRedactedThinkingDroppedOnToolLoopReplay(t *testing.T) {
	m := runtimeHuntReplay(t, `[{"type":"redacted_thinking","data":"ENCRYPTED-OPAQUE"},{"type":"tool_use","id":"tu1","name":"f","input":{}}]`)
	raw, _ := json.Marshal(m)
	if !strings.Contains(string(raw), `"redacted_thinking"`) || !strings.Contains(string(raw), "ENCRYPTED-OPAQUE") {
		t.Fatalf("replayed assistant turn lost the redacted_thinking block: %s", raw)
	}
}

// With interleaved thinking a response carries several signed thinking
// blocks. mapResult keeps only the joined text and the LAST signature, so
// the replay sends one thinking block whose text no longer matches its
// signature — every signed segment must be replayed verbatim.
func TestRED_AnthropicMultipleThinkingBlocksCollapsedOnReplay(t *testing.T) {
	m := runtimeHuntReplay(t, `[{"type":"thinking","thinking":"first","signature":"SIG-ONE"},{"type":"thinking","thinking":"second","signature":"SIG-TWO"},{"type":"tool_use","id":"tu1","name":"f","input":{}}]`)
	raw, _ := json.Marshal(m)
	if !strings.Contains(string(raw), "SIG-ONE") || !strings.Contains(string(raw), "SIG-TWO") {
		t.Fatalf("replayed assistant turn did not carry every signed thinking block: %s", raw)
	}
}

// Signed reasoning belongs to the provider that issued it: a turn recorded
// for another API format keeps its answer and tool calls but loses the
// signed blocks and signature when replayed.
func TestRED_RuntimeThinkingBlocksDroppedAcrossProviders(t *testing.T) {
	in := []session.Message{{
		Role:             "assistant",
		Content:          "x",
		ThinkingProvider: "openai",
		ThinkingBlocks:   []session.ThinkingBlock{{Text: "t", Signature: "SIG"}},
		ToolCalls:        []session.ToolCall{{ID: "a", Type: "function"}},
	}}
	_, got := toSDKMessagesFor(in, false, true, "anthropic")
	if len(got) != 1 || len(got[0].ThinkingBlocks) != 0 || got[0].ThinkingSignature != "" || got[0].ReasoningContent != "" {
		t.Fatalf("foreign-provider signed reasoning was replayed: %+v", got)
	}
	in[0].ThinkingProvider = "anthropic"
	_, got = toSDKMessagesFor(in, false, true, "anthropic")
	if len(got[0].ThinkingBlocks) != 1 || got[0].ThinkingBlocks[0].Signature != "SIG" {
		t.Fatalf("same-provider blocks not replayed: %+v", got)
	}
}
