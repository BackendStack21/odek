package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// Side calls (compaction digest refresh, budget progress summary) are real
// LLM calls: they consume provider tokens and real cost. The engine
// contract (see Run: "totals are per-run and feed budget enforcement")
// requires that usage to be visible to the totals — otherwise
// max_input_tokens / max_output_tokens / max_cost_usd caps are silently
// exceedable and usage reporting under-counts.

// TestSummarizeDropped_UsageCounted: the compaction side call's
// provider-reported usage must land in the engine totals.
func TestSummarizeDropped_UsageCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetFinalResponse("digest summary", 111, 222))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)

	engine.summarizeDropped(context.Background(), []llm.Message{
		{Role: "assistant", Content: "dropped work"},
	})
	if engine.TotalInputTokens != 111 {
		t.Errorf("TotalInputTokens = %d, want 111 (side-call usage must be counted)", engine.TotalInputTokens)
	}
	if engine.TotalOutputTokens != 222 {
		t.Errorf("TotalOutputTokens = %d, want 222 (side-call usage must be counted)", engine.TotalOutputTokens)
	}
}

// TestSummarizeProgress_UsageCounted: the post-loop progress-summary side
// call's usage must land in the engine totals.
func TestSummarizeProgress_UsageCounted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetFinalResponse("progress summary", 55, 66))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)

	engine.summarizeProgress(context.Background(), []llm.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "partial work"},
	})
	if engine.TotalInputTokens != 55 {
		t.Errorf("TotalInputTokens = %d, want 55 (side-call usage must be counted)", engine.TotalInputTokens)
	}
	if engine.TotalOutputTokens != 66 {
		t.Errorf("TotalOutputTokens = %d, want 66 (side-call usage must be counted)", engine.TotalOutputTokens)
	}
}

// TestRun_SideCallTokensEnforceBudget: a digest side call that pushes the
// run over max_input_tokens must trip the typed budget error on the next
// main-path check — not slip through invisibly.
func TestRun_SideCallTokensEnforceBudget(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// Compaction digest side call: heavy usage.
			fmt.Fprint(w, budgetFinalResponse("digest summary", 200, 5))
			return
		}
		// Main-path responses.
		fmt.Fprint(w, budgetFinalResponse("the answer", 250, 5))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "sys", nil, 3000)
	engine.SetCompaction(true) // engine default is off; config enables it
	engine.SetLimits(budget.Limits{MaxInputTokens: 400}, "test-model")

	// Heavy old groups force pass-2 group drops at the top of iteration 0
	// (same shape as TestTrimContext_*): the drop triggers the digest
	// side call before the first main LLM call.
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	for i := 0; i < 20; i++ {
		tc := llm.ToolCall{ID: fmt.Sprintf("c%d", i), Type: "function"}
		tc.Function.Name = "echo"
		tc.Function.Arguments = "{}"
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: strings.Repeat("x", 300), ToolCalls: []llm.ToolCall{tc}},
			llm.Message{Role: "tool", Content: strings.Repeat("y", 900), ToolCallID: fmt.Sprintf("c%d", i)},
		)
	}

	_, _, err := engine.RunWithMessages(context.Background(), msgs)
	berr, ok := budget.As(err)
	if !ok {
		t.Fatalf("expected typed budget.Error (side-call + main usage = 450 > cap 400), got err=%v", err)
	}
	if berr.Limit != budget.LimitInputTokens {
		t.Errorf("budget limit = %v, want input_tokens", berr.Limit)
	}
}

// TestRefreshDigest_SkipsSideCallWhenBudgetExhausted: the digest refresh
// must respect budgetAllowsSideCall — the post-loop summary already does;
// the in-loop digest side call must too, or an exhausted run keeps
// spending on side calls.
func TestRefreshDigest_SkipsSideCallWhenBudgetExhausted(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, budgetFinalResponse("digest summary", 10, 5))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxInputTokens: 100}, "test-model")
	// SetLimits stores the limits; the checker itself is built at runLoop
	// start. refreshDigest is called outside a run here, so install the
	// checker the same way runLoop does.
	engine.budget = budget.NewChecker(budget.Limits{MaxInputTokens: 100}, time.Now())
	// Budget already blown by the main path.
	engine.TotalInputTokens = 10_000
	engine.TotalOutputTokens = 5_000

	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	dropped := []llm.Message{{Role: "assistant", Content: "old work"}}

	out := engine.refreshDigest(context.Background(), msgs, dropped)
	if n := calls.Load(); n != 0 {
		t.Fatalf("digest side call fired %d times with budget exhausted, want 0", n)
	}
	if len(out) != len(msgs) {
		t.Errorf("refreshDigest with skipped side call must return messages unchanged: got %d msgs, want %d", len(out), len(msgs))
	}
}
