package loop

// Tests for budget-awareness telemetry and graceful finalization
// (SUB_AGENTS_IMPROVEMENTS.md M1.2/M1.3).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// scriptedServer serves a sequence of LLM responses, one per request.
func scriptedServer(responses ...string) *httptest.Server {
	var idx atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(idx.Add(1)) - 1
		if i >= len(responses) {
			i = len(responses) - 1
		}
		fmt.Fprint(w, responses[i])
	}))
}

// gateTool blocks inside Call until released, signalling entry first.
type gateTool struct {
	entered chan struct{}
	release chan struct{}
}

func (t *gateTool) Name() string        { return "block" }
func (t *gateTool) Description() string { return "blocking test tool" }
func (t *gateTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *gateTool) Call(args string) (string, error) {
	close(t.entered)
	<-t.release
	return "ok", nil
}

func TestBudgetWarnings_FiresOncePerThreshold(t *testing.T) {
	e := &Engine{maxIter: 10}
	st := budgetHintState{}
	ctx := context.Background() // no deadline → iteration dimension only

	start := time.Now()
	if got := e.budgetWarnings(5, start, ctx, &st); len(got) != 1 {
		t.Fatalf("completed=5/10: got %d hints, want 1 (50%%)", len(got))
	}
	if got := e.budgetWarnings(5, start, ctx, &st); len(got) != 0 {
		t.Fatalf("same threshold must not refire, got %d hints", len(got))
	}
	if got := e.budgetWarnings(7, start, ctx, &st); len(got) != 0 {
		t.Fatalf("completed=7/10 = 70%%: no threshold, got %d hints", len(got))
	}
	if got := e.budgetWarnings(8, start, ctx, &st); len(got) != 1 {
		t.Fatalf("completed=8/10: got %d hints, want 1 (75%%)", len(got))
	}
	if got := e.budgetWarnings(9, start, ctx, &st); len(got) != 1 {
		t.Fatalf("completed=9/10: got %d hints, want 1 (90%%)", len(got))
	}
	if got := e.budgetWarnings(10, start, ctx, &st); len(got) != 0 {
		t.Fatalf("all thresholds fired, got %d more", len(got))
	}
}

func TestBudgetWarnings_DisabledWithoutLimits(t *testing.T) {
	e := &Engine{maxIter: 0}
	st := budgetHintState{}
	if got := e.budgetWarnings(50, time.Now(), context.Background(), &st); len(got) != 0 {
		t.Fatalf("no iteration cap and no deadline: got %d hints, want 0", len(got))
	}
}

func TestBudgetWarnings_WallClockDimension(t *testing.T) {
	start := time.Now()
	clock := start
	e := &Engine{maxIter: 0, budgetNow: func() time.Time { return clock }} // time dimension only
	st := budgetHintState{}
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(10*time.Second))
	defer cancel()

	// 20% of the window elapsed: nothing fires.
	if got := e.budgetWarnings(0, start, ctx, &st); len(got) != 0 {
		t.Fatalf("20%% elapsed: got %d hints, want 0", len(got))
	}
	// 60% elapsed: the 50% threshold fires via the wall clock.
	clock = start.Add(6 * time.Second)
	if got := e.budgetWarnings(0, start, ctx, &st); len(got) != 1 {
		t.Fatalf("60%% elapsed: got %d hints, want 1 (50%%)", len(got))
	}
	// 95% elapsed: both remaining thresholds fire.
	clock = start.Add(9500 * time.Millisecond)
	if got := e.budgetWarnings(0, start, ctx, &st); len(got) != 2 {
		t.Fatalf("95%% elapsed: got %d hints, want 2 (75%%, 90%%)", len(got))
	}
}

func TestPartialSummaryReason_Markers(t *testing.T) {
	cases := []struct {
		in     string
		reason string
		ok     bool
	}{
		{"[Iteration budget reached — partial summary]\n\nstuff", "iteration_budget", true},
		{"[Execution budget reached — partial summary]\n\nstuff", "execution_budget", true},
		{"[Time budget reached — partial summary]\n\nstuff", "time_budget", true},
		{"final answer", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		reason, ok := PartialSummaryReason(tc.in)
		if ok != tc.ok || reason != tc.reason {
			t.Errorf("PartialSummaryReason(%q) = (%q, %v), want (%q, %v)", tc.in, reason, ok, tc.reason, tc.ok)
		}
	}
}

func TestEngine_RequestFinalization_GracefulTimeBudgetSummary(t *testing.T) {
	server := scriptedServer(
		budgetToolCallResponse("call_1", "block", 10, 10),
		budgetFinalResponse("partial work done", 10, 10),
	)
	defer server.Close()

	bt := &gateTool{entered: make(chan struct{}), release: make(chan struct{})}
	engine := New(llm.New(server.URL, "sk-test", "test-model", "", 0, 0),
		tool.NewRegistry([]tool.Tool{bt}), 10, "", nil, 0)

	type runResult struct {
		res string
		err error
	}
	ch := make(chan runResult, 1)
	go func() {
		res, err := engine.Run(context.Background(), "do work")
		ch <- runResult{res, err}
	}()

	select {
	case <-bt.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tool was never entered")
	}
	// Soft deadline fires mid-tool: the engine must conclude at the next
	// iteration boundary with the time-budget partial summary, not run to
	// the iteration cap and not hang.
	engine.RequestFinalization()
	close(bt.release)

	select {
	case rr := <-ch:
		if rr.err != nil {
			t.Fatalf("Run returned error %v, want graceful partial summary", rr.err)
		}
		if len(rr.res) == 0 || rr.res[:len(timeBudgetSummaryMarker)] != timeBudgetSummaryMarker {
			t.Fatalf("result = %q, want prefix %q", rr.res, timeBudgetSummaryMarker)
		}
		reason, ok := PartialSummaryReason(rr.res)
		if !ok || reason != "time_budget" {
			t.Errorf("PartialSummaryReason = (%q, %v), want (time_budget, true)", reason, ok)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after finalization request")
	}
}
