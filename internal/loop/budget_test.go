package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// budgetToolCallResponse renders a fake LLM response that requests one call
// to the given tool, with the given token usage.
func budgetToolCallResponse(id, toolName string, promptTokens, completionTokens int) string {
	return fmt.Sprintf(`{
		"choices":[{"message":{"content":"working","tool_calls":[{
			"id":%q,"type":"function",
			"function":{"name":%q,"arguments":"{}"}
		}]}}],
		"usage":{"prompt_tokens":%d,"completion_tokens":%d}
	}`, id, toolName, promptTokens, completionTokens)
}

// budgetFinalResponse renders a fake LLM final answer with the given usage.
func budgetFinalResponse(content string, promptTokens, completionTokens int) string {
	return fmt.Sprintf(`{
		"choices":[{"message":{"content":%q}}],
		"usage":{"prompt_tokens":%d,"completion_tokens":%d}
	}`, content, promptTokens, completionTokens)
}

// countingTool counts invocations.
type countingTool struct{ calls atomic.Int64 }

func (t *countingTool) Name() string        { return "count" }
func (t *countingTool) Description() string { return "counting test tool" }
func (t *countingTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *countingTool) Call(args string) (string, error) {
	t.calls.Add(1)
	return "ok", nil
}

// eventRecorder collects emitted events in order.
type eventRecorder struct {
	mu  sync.Mutex
	evs []events.Event
}

func (r *eventRecorder) add(ev events.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
}

func (r *eventRecorder) find(typ string) *events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.evs {
		if r.evs[i].Type == typ {
			return &r.evs[i]
		}
	}
	return nil
}

func TestEngine_Budget_InputTokensExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 1000, 10))
	}))
	defer server.Close()

	ct := &countingTool{}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{ct}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxInputTokens: 500}, "test-model")

	var persisted [][]llm.Message
	engine.SetMessagesPersistCallback(func(msgs []llm.Message) {
		persisted = append(persisted, msgs)
	})
	rec := &eventRecorder{}
	engine.SetEventHandler(rec.add)

	_, err := engine.Run(context.Background(), "do work")
	berr, ok := budget.As(err)
	if !ok {
		t.Fatalf("expected typed budget.Error, got %v", err)
	}
	if berr.Limit != budget.LimitInputTokens || berr.Observed != 1000 || berr.Maximum != 500 {
		t.Errorf("unexpected budget error: %+v", berr)
	}
	if ct.calls.Load() != 0 {
		t.Error("tool must not execute after the input-token budget fired")
	}
	// Persist callback fired before return, with a safe (dangling-free) state.
	if len(persisted) == 0 {
		t.Fatal("persist callback must fire before the budget error returns")
	}
	last := persisted[len(persisted)-1]
	for _, m := range last {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			t.Error("persisted state must not contain unanswered tool calls")
		}
	}
	// budget_exceeded event carries limit_name/observed/limit.
	ev := rec.find(events.TypeBudgetExceeded)
	if ev == nil {
		t.Fatal("budget_exceeded event not emitted")
	}
	if ev.Data["limit_name"] != budget.LimitInputTokens {
		t.Errorf("limit_name = %v", ev.Data["limit_name"])
	}
	if ev.Data["observed"] != int64(1000) || ev.Data["limit"] != int64(500) {
		t.Errorf("observed/limit = %v/%v", ev.Data["observed"], ev.Data["limit"])
	}
}

func TestEngine_Budget_OutputTokensExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 10, 600))
	}))
	defer server.Close()

	ct := &countingTool{}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{ct}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxOutputTokens: 500}, "test-model")

	_, err := engine.Run(context.Background(), "do work")
	berr, ok := budget.As(err)
	if !ok || berr.Limit != budget.LimitOutputTokens {
		t.Fatalf("expected output_tokens budget error, got %v", err)
	}
	if ct.calls.Load() != 0 {
		t.Error("tool must not execute after the output-token budget fired")
	}
}

func TestEngine_Budget_ToolCallsExceededBeforeExecution(t *testing.T) {
	var callNum atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callNum.Add(1)
		switch n {
		case 1:
			// First iteration: one tool call (allowed — reaches the cap).
			fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 10, 10))
		case 2:
			// Second iteration: one more tool call (would exceed the cap).
			fmt.Fprint(w, budgetToolCallResponse("call_2", "count", 10, 10))
		default:
			// The partial-summary side call (allowed: only tool_calls is
			// exhausted, and a tool-less summary consumes no tool budget).
			fmt.Fprint(w, budgetFinalResponse("partial progress summary", 10, 10))
		}
	}))
	defer server.Close()

	ct := &countingTool{}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{ct}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxToolCalls: 1}, "test-model")

	var persisted [][]llm.Message
	engine.SetMessagesPersistCallback(func(msgs []llm.Message) {
		persisted = append(persisted, msgs)
	})
	rec := &eventRecorder{}
	engine.SetEventHandler(rec.add)

	_, messages, err := engine.RunWithMessages(context.Background(), []llm.Message{{Role: "user", Content: "do work"}})
	berr, ok := budget.As(err)
	if !ok {
		t.Fatalf("expected typed budget.Error, got %v", err)
	}
	if berr.Limit != budget.LimitToolCalls || berr.Maximum != 1 {
		t.Errorf("unexpected budget error: %+v", berr)
	}
	if got := ct.calls.Load(); got != 1 {
		t.Errorf("tool executed %d times, want exactly 1 (cap reached, second batch refused)", got)
	}
	// Partial summary was within budget (tool-less), so it was appended and
	// persisted before the typed error returned.
	var foundSummary bool
	for _, m := range messages {
		if strings.HasPrefix(m.Content, execBudgetSummaryMarker) {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Error("partial summary should be appended when only the tool-call budget is exhausted")
	}
	if len(persisted) == 0 {
		t.Fatal("persist callback must fire before the budget error returns")
	}
	if ev := rec.find(events.TypeBudgetExceeded); ev == nil || ev.Data["limit_name"] != budget.LimitToolCalls {
		t.Errorf("budget_exceeded event missing or wrong: %+v", ev)
	}
}

func TestEngine_Budget_RuntimeExceeded(t *testing.T) {
	// Fake clock: starts at t0; the tool call advances it past the limit, so
	// the runtime check before the next LLM call fires.
	var mu sync.Mutex
	now := time.Now()
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 10, 10))
	}))
	defer server.Close()

	// Advance the clock from inside the tool: after the first batch the fake
	// wall clock is 120s in, past the 60s limit.
	advanceTool := &callbackTool{fn: func() { advance(120 * time.Second) }}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{advanceTool}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxRuntimeSeconds: 60}, "test-model")
	engine.budgetNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	rec := &eventRecorder{}
	engine.SetEventHandler(rec.add)

	_, err := engine.Run(context.Background(), "do work")
	berr, ok := budget.As(err)
	if !ok {
		t.Fatalf("expected typed budget.Error, got %v", err)
	}
	if berr.Limit != budget.LimitRuntime || berr.Maximum != 60 {
		t.Errorf("unexpected budget error: %+v", berr)
	}
	if got := advanceTool.calls; got != 1 {
		t.Errorf("tool executed %d times, want 1 (runtime fired before the next LLM call)", got)
	}
	if ev := rec.find(events.TypeBudgetExceeded); ev == nil || ev.Data["limit_name"] != budget.LimitRuntime {
		t.Errorf("budget_exceeded event missing or wrong: %+v", ev)
	}
}

// callbackTool runs fn on each call.
type callbackTool struct {
	fn    func()
	calls int64
}

func (t *callbackTool) Name() string        { return "count" }
func (t *callbackTool) Description() string { return "callback test tool" }
func (t *callbackTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *callbackTool) Call(args string) (string, error) {
	t.calls++
	if t.fn != nil {
		t.fn()
	}
	return "ok", nil
}

func TestEngine_Budget_CostExceededWithPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 60k in ($0.06) + 50k out ($0.05) = $0.11 >= $0.10 cap.
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 60_000, 50_000))
	}))
	defer server.Close()

	ct := &countingTool{}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{ct}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{
		MaxCostUSD:              0.10,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 1.0,
	}, "test-model")
	rec := &eventRecorder{}
	engine.SetEventHandler(rec.add)

	_, err := engine.Run(context.Background(), "do work")
	berr, ok := budget.As(err)
	if !ok || berr.Limit != budget.LimitCostUSD {
		t.Fatalf("expected cost_usd budget error, got %v", err)
	}
	if ct.calls.Load() != 0 {
		t.Error("tool must not execute after the cost budget fired")
	}
	ev := rec.find(events.TypeBudgetExceeded)
	if ev == nil {
		t.Fatal("budget_exceeded event not emitted")
	}
	// Cost values are reported in USD, not micro-USD.
	if obs, okv := ev.Data["observed"].(float64); !okv || obs < 0.109 || obs > 0.111 {
		t.Errorf("observed = %v, want ~0.11 USD", ev.Data["observed"])
	}
	if lim, okv := ev.Data["limit"].(float64); !okv || lim != 0.10 {
		t.Errorf("limit = %v, want 0.10 USD", ev.Data["limit"])
	}
}

func TestEngine_Budget_CostDisabledWithoutPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetFinalResponse("done", 1_000_000, 1_000_000))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)
	// Absurdly low cost cap, but no prices configured: cost enforcement must
	// stay off and the run must complete.
	engine.SetLimits(budget.Limits{MaxCostUSD: 0.000001}, "test-model")

	result, err := engine.Run(context.Background(), "do work")
	if err != nil {
		t.Fatalf("run should complete with cost enforcement disabled: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want %q", result, "done")
	}
}

func TestEngine_Budget_IterationSummarySkippedWhenTokensExhausted(t *testing.T) {
	// Token budget fires mid-run; when the iteration cap is ALSO reached the
	// partial-summary side call must not fire (it would itself exceed the
	// budget). Here the token budget fires first, so runLoop returns the
	// budget error directly — this pins that no summary call is attempted
	// after token exhaustion.
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 1000, 10))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{&countingTool{}}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{MaxInputTokens: 500}, "test-model")

	_, err := engine.Run(context.Background(), "do work")
	if _, ok := budget.As(err); !ok {
		t.Fatalf("expected budget.Error, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("LLM called %d times, want 1 (no summary side call after token exhaustion)", got)
	}
}

func TestEngine_Budget_CostExceededWithModelPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 60k in + 50k out. Flat prices ($1/$1) estimate $0.11 — under the
		// $0.20 cap — but the model-specific prices ($3/$3) estimate $0.33,
		// so the cap must fire at the model-specific price point.
		fmt.Fprint(w, budgetToolCallResponse("call_1", "count", 60_000, 50_000))
	}))
	defer server.Close()

	ct := &countingTool{}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{ct}), 10, "", nil, 0)
	engine.SetLimits(budget.Limits{
		MaxCostUSD:              0.20,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 1.0,
		ModelPrices: map[string]budget.ModelPrice{
			"test-model": {InputCostPerMillionUSD: 3.0, OutputCostPerMillionUSD: 3.0},
		},
	}, "test-model")
	rec := &eventRecorder{}
	engine.SetEventHandler(rec.add)

	_, err := engine.Run(context.Background(), "do work")
	berr, ok := budget.As(err)
	if !ok || berr.Limit != budget.LimitCostUSD {
		t.Fatalf("expected cost_usd budget error at the model-specific price, got %v", err)
	}
	if ct.calls.Load() != 0 {
		t.Error("tool must not execute after the cost budget fired")
	}
	if got := budget.MicroToUSD(berr.Observed); got < 0.329 || got > 0.331 {
		t.Errorf("observed = $%f, want ~$0.33 (model-specific prices)", got)
	}
	ev := rec.find(events.TypeBudgetExceeded)
	if ev == nil || ev.Data["limit_name"] != budget.LimitCostUSD {
		t.Fatalf("budget_exceeded event missing or wrong: %+v", ev)
	}
	if obs, okv := ev.Data["observed"].(float64); !okv || obs < 0.329 || obs > 0.331 {
		t.Errorf("event observed = %v, want ~0.33 USD", ev.Data["observed"])
	}
}

func TestEngine_Budget_CostFlatPricesForUnmatchedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, budgetFinalResponse("done", 60_000, 50_000))
	}))
	defer server.Close()

	engine := New(llm.New(server.URL, "sk-test", "other-model", "", 0, 0),
		tool.NewRegistry(nil), 10, "", nil, 0)
	// Same limits as the model-price test above, but the run's model does not
	// match the model_prices key: the flat pair estimates $0.11 < $0.20 cap,
	// so the run must complete.
	engine.SetLimits(budget.Limits{
		MaxCostUSD:              0.20,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 1.0,
		ModelPrices: map[string]budget.ModelPrice{
			"test-model": {InputCostPerMillionUSD: 3.0, OutputCostPerMillionUSD: 3.0},
		},
	}, "other-model")

	result, err := engine.Run(context.Background(), "do work")
	if err != nil {
		t.Fatalf("run should complete at flat prices for an unmatched model: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want %q", result, "done")
	}
}
