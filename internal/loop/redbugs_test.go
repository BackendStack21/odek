package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// RED #12 (L1): refreshDigest inserts the digest right after the protected
// head — i.e. AFTER the first user message (headLen includes the task).
// trimToSurvival only scans for the digest BEFORE the first user message,
// so a freshly created digest is never found and the compacted history is
// silently discarded exactly under context pressure.
func TestRED_TrimToSurvivalKeepsDigest(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "original task"},
		{Role: "system", Content: digestMsgPrefix + " summary of earlier turns.]"}, // digest position per refreshDigest
		{Role: "assistant", Content: big},
		{Role: "tool", Content: big},
		{Role: "user", Content: "latest question"},
	}
	out := trimToSurvival(msgs)
	found := false
	for _, m := range out {
		if isDigestMessage(m) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("trimToSurvival dropped the rolling compaction digest; output has %d messages, none digest-marked", len(out))
	}
}

func newUsageServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
	}))
}

// RED #13 (L2): Engine.Run does not reset token accounting even though its
// own field contract says "Reset on each Run/RunWithMessages call". A second
// Run accumulates totals from the first, so a per-run input-token budget
// trips early (or callers read cross-run sums).
func TestRED_RunResetsTokenAccounting(t *testing.T) {
	calls := 0
	server := newUsageServer(t, &calls)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxInputTokens: 150}, "test-model")

	if _, err := engine.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first Run error: %v", err)
	}
	firstTotal := engine.TotalInputTokens

	if _, err := engine.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second Run failed: %v — Run must reset per-run token accounting", err)
	}
	if engine.TotalInputTokens != firstTotal {
		t.Errorf("after second Run TotalInputTokens = %d, want %d (reset per run)", engine.TotalInputTokens, firstTotal)
	}
}

// RED #14 (L5): RunWithMessages with an empty history plus a memory
// callback panics: insertAt := 1 slices past an empty message list.
func TestRED_RunWithMessagesEmptyHistoryNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunWithMessages(nil history + memory func) panicked: %v", r)
		}
	}()

	calls := 0
	server := newUsageServer(t, &calls)
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	engine := New(client, tool.NewRegistry(nil), 10, "", nil, 0)
	engine.SetMemoryPromptFunc(func() string { return "memory block" })

	_, _, _ = engine.RunWithMessages(context.Background(), nil)
}
