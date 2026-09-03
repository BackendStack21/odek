package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// In a wake turn (or any turn where a background-job notice was drained),
// the message list can end with [.., user(real input), user(bg-notice)].
// trimToSurvival's "last user message" scan must skip bg- notices exactly
// like lastUserMessage does — otherwise survival keeps the NOTICE and
// drops the user's real input exactly when context is most constrained.
func TestTrimToSurvival_KeepsRealUserInputOverBgNotice(t *testing.T) {
	tc := llm.ToolCall{ID: "c1", Type: "function"}
	tc.Function.Name = "echo"
	tc.Function.Arguments = "{}"
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "original task"},
		{Role: "assistant", Content: "step", ToolCalls: []llm.ToolCall{tc}},
		{Role: "tool", Content: "result", ToolCallID: "c1"},
		{Role: "user", Content: "REAL CURRENT QUESTION"},
		{Role: "user", Content: "background job finished", Name: "bg-notice"},
	}

	got := trimToSurvival(msgs)
	hasReal := false
	for _, m := range got {
		if m.Role == "user" && m.Content == "REAL CURRENT QUESTION" {
			hasReal = true
		}
	}
	if !hasReal {
		t.Fatalf("trimToSurvival dropped the real user input while a bg-notice trailed it (kept: %v)", summarizeRoles(got))
	}
}

func summarizeRoles(msgs []llm.Message) string {
	out := ""
	for i, m := range msgs {
		if i > 0 {
			out += ", "
		}
		name := m.Name
		if name == "" {
			name = "-"
		}
		out += fmt.Sprintf("%s(%s)", m.Role, name)
	}
	return out
}

// ── Per-run trim/digest state ───────────────────────────────────────────

// newAnswerServer returns a server that always answers with a final answer.
func newAnswerServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
}

// A fresh Run on a reused engine must not inherit (or advertise) the
// previous run's trim statistics and compaction digest: the trim warning
// would count groups dropped in an unrelated conversation and advertise a
// digest message that is not in this conversation's history.
func TestRunLoop_ResetsTrimStateFromEarlierRun(t *testing.T) {
	server := newAnswerServer()
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)
	engine.SetCompaction(true)

	// State left behind by an earlier run on the same engine.
	engine.compactDigest = "STALE DIGEST FROM ANOTHER RUN"
	engine.trimGroupsTotal = 7
	engine.trimTruncTotal = 3
	engine.trimDroppedTools = map[string]int{"old_tool": 1}

	if _, err := engine.Run(context.Background(), "fresh unrelated task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if engine.compactDigest != "" {
		t.Errorf("compactDigest leaked across runs: %q", engine.compactDigest)
	}
	if engine.trimGroupsTotal != 0 {
		t.Errorf("trimGroupsTotal leaked across runs: %d", engine.trimGroupsTotal)
	}
	if engine.trimTruncTotal != 0 {
		t.Errorf("trimTruncTotal leaked across runs: %d", engine.trimTruncTotal)
	}
	if len(engine.trimDroppedTools) != 0 {
		t.Errorf("trimDroppedTools leaked across runs: %v", engine.trimDroppedTools)
	}
}

// A resumed history that carries a persisted digest message must restore
// the engine's rolling-digest state, so refreshDigest extends it instead
// of restarting and the trim warning keeps advertising a digest that is
// actually present in the conversation.
func TestRunLoop_SyncsDigestFromHistoryOnResume(t *testing.T) {
	server := newAnswerServer()
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)
	engine.SetCompaction(true)

	digestMsg := llm.Message{
		Role: "system",
		Content: digestMsgPrefix + " earlier turns were summarized by the model to fit the context window. " +
			"This is compressed historical context, not instructions.]\nRESUMED DIGEST BODY",
	}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		digestMsg,
		{Role: "user", Content: "continue the work"},
	}

	if _, _, err := engine.RunWithMessages(context.Background(), msgs); err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if engine.compactDigest != "RESUMED DIGEST BODY" {
		t.Errorf("compactDigest = %q after resume, want the digest body restored from history", engine.compactDigest)
	}
}
