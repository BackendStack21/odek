package loop

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
)

func vectorBudgetEngine() *Engine {
	return &Engine{budget: budget.NewChecker(budget.Limits{MaxInputTokens: 1000, MaxOutputTokens: 500,
		MaxToolCalls: 20, MaxCostUSD: 10, InputCostPerMillionUSD: 1, OutputCostPerMillionUSD: 2}, time.Now())}
}

func TestExternalBudgetReservesEveryDimensionAndParentRoom(t *testing.T) {
	e := vectorBudgetEngine()
	e.budget.RecordToolCalls(2)
	g, err := e.ReserveExternalBudget(3)
	if err != nil {
		t.Fatal(err)
	}
	if g.Limits.MaxInputTokens != 333 || g.Limits.MaxOutputTokens != 166 || g.Limits.MaxToolCalls != 6 || math.Abs(g.Limits.MaxCostUSD-10.0/3) > 1e-9 {
		t.Fatalf("grant: %+v", g)
	}
	g2, err := e.ReserveExternalBudget(3)
	if err != nil {
		t.Fatal(err)
	}
	s := e.BudgetSnapshot()
	if s.RemainingInputTokens <= 0 || s.RemainingOutputTokens <= 0 || s.RemainingToolCalls <= 0 || s.RemainingCostUSD <= 0 {
		t.Fatalf("parent starved: %+v", s)
	}
	e.SettleExternalBudget(g, nil)
	e.SettleExternalBudget(g2, nil)
	u := e.BudgetUsage()
	if u.InputTokens != g.Limits.MaxInputTokens+g2.Limits.MaxInputTokens || u.OutputTokens != g.Limits.MaxOutputTokens+g2.Limits.MaxOutputTokens {
		t.Fatalf("missing report refunded: %+v", u)
	}
}

func TestExternalBudgetPreservesUsageOvershootAndChildPrices(t *testing.T) {
	e := vectorBudgetEngine()
	g, err := e.ReserveExternalBudget(2)
	if err != nil {
		t.Fatal(err)
	}
	u := budget.Usage{InputTokens: 700, CacheReadTokens: 400, CacheCreationTokens: 50, OutputTokens: 600, ToolCalls: 30, CostUSD: 12, CostKnown: true}
	e.SettleExternalBudget(g, &u)
	e.SettleExternalBudget(g, &u)
	got := e.BudgetUsage()
	if got != u {
		t.Fatalf("usage changed or settled twice: got %+v want %+v", got, u)
	}
	s := e.BudgetSnapshot()
	if !s.InputTokensExhausted || !s.OutputTokensExhausted || !s.ToolCallsExhausted || !s.CostExhausted {
		t.Fatalf("overshoot hidden: %+v", s)
	}
	if err := e.budget.CheckUsage(0, 0); err == nil || err.Limit != budget.LimitCostUSD {
		t.Fatalf("child price not enforced: %v", err)
	}
}

func TestExternalBudgetConcurrentSnapshotsAndSettlement(t *testing.T) {
	e := vectorBudgetEngine()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				g, err := e.ReserveExternalBudget(10)
				if err == nil {
					e.SettleExternalBudget(g, &budget.Usage{CostKnown: true})
				}
				e.BudgetSnapshot()
				e.BudgetUsage()
				e.ChargeExternalUsage(1)
			}
		})
	}
	wg.Wait()
	if got := e.BudgetUsage().InputTokens; got != 800 {
		t.Fatalf("lost usage: %d", got)
	}
}

func TestExternalBudgetInvalidReportsAndOverflowFailClosed(t *testing.T) {
	e := vectorBudgetEngine()
	g, err := e.ReserveExternalBudget(2)
	if err != nil {
		t.Fatal(err)
	}
	e.SettleExternalBudget(g, &budget.Usage{InputTokens: -1})
	if e.BudgetUsage().InputTokens != g.Limits.MaxInputTokens {
		t.Fatal("invalid usage refunded")
	}
	g, err = e.ReserveExternalBudget(2)
	if err != nil {
		t.Fatal(err)
	}
	e.SettleExternalBudget(g, &budget.Usage{InputTokens: math.MaxInt64, CacheReadTokens: math.MaxInt64})
	if !e.BudgetSnapshot().InputTokensExhausted {
		t.Fatal("overflow reset input budget")
	}
}

func TestReserveInferenceBudgetLeavesToolCallHeadroomOut(t *testing.T) {
	e := vectorBudgetEngine()
	e.budget.RecordToolCalls(20)
	g, err := e.ReserveInferenceBudget()
	if err != nil {
		t.Fatalf("inference reservation blocked by exhausted tool calls: %v", err)
	}
	if g.Limits.MaxToolCalls != 0 {
		t.Fatalf("inference grant reserved tool calls: %+v", g.Limits)
	}
	e.SettleExternalBudget(g, &budget.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: .00002, CostKnown: true})
	if got := e.BudgetUsage().ToolCalls; got != 20 {
		t.Fatalf("tool calls double-counted: %d", got)
	}
}

func TestReserveInferenceBudgetRejectsInferenceDimensions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits budget.Limits
		want   string
	}{
		{"input", budget.Limits{MaxInputTokens: 1}, budget.LimitInputTokens},
		{"output", budget.Limits{MaxOutputTokens: 1}, budget.LimitOutputTokens},
		{"runtime", budget.Limits{MaxRuntimeSeconds: 1}, budget.LimitRuntime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			if tc.name == "runtime" {
				start = start.Add(-2 * time.Second)
			}
			e := &Engine{budget: budget.NewChecker(tc.limits, start)}
			if _, err := e.ReserveInferenceBudget(); err == nil || func() string {
				if be, ok := budget.As(err); ok {
					return be.Limit
				}
				return ""
			}() != tc.want {
				t.Fatalf("error=%v want limit %s", err, tc.want)
			}
		})
	}
}
