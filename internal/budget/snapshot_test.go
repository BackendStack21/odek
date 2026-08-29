package budget

import (
	"testing"
	"time"
)

func TestCheckerSnapshot_NilChecker(t *testing.T) {
	var c *Checker
	s := c.Snapshot(1, 2)
	if s != (Snapshot{}) {
		t.Errorf("nil checker Snapshot = %+v, want zero value", s)
	}
}

func TestCheckerSnapshot_Remaining(t *testing.T) {
	start := time.Now()
	clock := start
	c := NewChecker(Limits{
		MaxRuntimeSeconds:       100,
		MaxToolCalls:            10,
		MaxInputTokens:          1000,
		MaxOutputTokens:         500,
		MaxCostUSD:              1.0,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 1.0,
	}, start)
	c.SetNowFunc(func() time.Time { return clock })
	c.RecordToolCalls(3)

	s := c.Snapshot(400, 200)
	if s.MaxRuntimeSeconds != 100 || s.RemainingRuntimeSeconds != 100 {
		t.Errorf("runtime = %d/%d, want 100/100 (no elapsed)", s.RemainingRuntimeSeconds, s.MaxRuntimeSeconds)
	}
	if s.MaxToolCalls != 10 || s.RemainingToolCalls != 7 {
		t.Errorf("tool calls = %d/%d, want 7/10", s.RemainingToolCalls, s.MaxToolCalls)
	}
	if s.MaxInputTokens != 1000 || s.RemainingInputTokens != 600 {
		t.Errorf("input tokens = %d/%d, want 600/1000", s.RemainingInputTokens, s.MaxInputTokens)
	}
	if s.MaxOutputTokens != 500 || s.RemainingOutputTokens != 300 {
		t.Errorf("output tokens = %d/%d, want 300/500", s.RemainingOutputTokens, s.MaxOutputTokens)
	}
	// 400*1/1M + 200*1/1M = 0.0006 USD spent of 1.0.
	if s.MaxCostUSD != 1.0 || diff(s.RemainingCostUSD, 0.9994) > 1e-9 {
		t.Errorf("cost = %f/%f, want ~0.9994/1.0", s.RemainingCostUSD, s.MaxCostUSD)
	}

	// Advance the fake clock: runtime headroom shrinks.
	clock = start.Add(40 * time.Second)
	s = c.Snapshot(400, 200)
	if s.RemainingRuntimeSeconds != 60 {
		t.Errorf("runtime remaining after 40s = %d, want 60", s.RemainingRuntimeSeconds)
	}

	// Exhaust a dimension: remaining clamps at 0, never negative.
	clock = start.Add(200 * time.Second)
	s = c.Snapshot(5000, 5000)
	if s.RemainingRuntimeSeconds != 0 {
		t.Errorf("runtime remaining after overrun = %d, want 0", s.RemainingRuntimeSeconds)
	}
	if s.RemainingInputTokens != 0 || s.RemainingOutputTokens != 0 {
		t.Errorf("token remaining after overrun = %d/%d, want 0/0", s.RemainingInputTokens, s.RemainingOutputTokens)
	}
}

func TestCheckerSnapshot_UnconfiguredDimensions(t *testing.T) {
	c := NewChecker(Limits{MaxToolCalls: 5}, time.Now())
	s := c.Snapshot(0, 0)
	if s.MaxRuntimeSeconds != 0 || s.MaxInputTokens != 0 || s.MaxCostUSD != 0 {
		t.Errorf("unconfigured dimensions must stay zero, got %+v", s)
	}
	if s.MaxToolCalls != 5 || s.RemainingToolCalls != 5 {
		t.Errorf("tool calls = %d/%d, want 5/5", s.RemainingToolCalls, s.MaxToolCalls)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
