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

func TestCheckerLimitsGetter(t *testing.T) {
	limits := Limits{MaxToolCalls: 7, MaxRuntimeSeconds: 90}
	c := NewChecker(limits, time.Now())
	if c == nil {
		t.Fatal("NewChecker returned nil for non-empty limits")
	}
	if got := c.Limits(); got.MaxToolCalls != 7 || got.MaxRuntimeSeconds != 90 {
		t.Errorf("Limits() = %+v, want the configured caps", got)
	}
}

// TestCheckerSnapshot_ExhaustedFlags pins the share-mode exhaustion fix:
// Snapshot must distinguish a CONFIGURED limit that is fully consumed
// (Exhausted flag set, Remaining clamped to 0) from an UNCONFIGURED one
// (Max 0, flag false). Budget passdown (delegate_tasks share mode) clamps
// children off this difference — an exhausted parent budget read as
// "unconfigured" would hand the child an unbounded run.
func TestCheckerSnapshot_ExhaustedFlags(t *testing.T) {
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
	c.RecordToolCalls(10) // tool-call budget fully consumed

	// Overrun runtime and tokens: remaining clamps to 0 AND the flags fire.
	clock = start.Add(200 * time.Second)
	s := c.Snapshot(5000, 5000)
	if !s.RuntimeExhausted {
		t.Error("RuntimeExhausted = false after 200s elapsed of a 100s limit, want true")
	}
	if !s.ToolCallsExhausted {
		t.Error("ToolCallsExhausted = false after 10/10 tool calls, want true")
	}
	if !s.InputTokensExhausted || !s.OutputTokensExhausted {
		t.Errorf("token exhaustion flags = %v/%v, want true/true after overrun",
			s.InputTokensExhausted, s.OutputTokensExhausted)
	}
	if s.RemainingRuntimeSeconds != 0 || s.RemainingToolCalls != 0 ||
		s.RemainingInputTokens != 0 || s.RemainingOutputTokens != 0 {
		t.Errorf("remaining must clamp at 0, got runtime=%d tools=%d in=%d out=%d",
			s.RemainingRuntimeSeconds, s.RemainingToolCalls, s.RemainingInputTokens, s.RemainingOutputTokens)
	}
	// ~0.01 USD spent of 1.0 — configured, with headroom, NOT exhausted.
	if s.CostExhausted {
		t.Errorf("CostExhausted = true with %.6f USD remaining, want false", s.RemainingCostUSD)
	}
}

// Exhaustion fires exactly at the limit boundary, not only on overrun.
func TestCheckerSnapshot_ExhaustedExactlyAtLimit(t *testing.T) {
	start := time.Now()
	clock := start
	c := NewChecker(Limits{MaxRuntimeSeconds: 100}, start)
	c.SetNowFunc(func() time.Time { return clock })
	clock = start.Add(100 * time.Second) // elapsed == limit
	s := c.Snapshot(0, 0)
	if !s.RuntimeExhausted || s.RemainingRuntimeSeconds != 0 {
		t.Errorf("exactly-at-limit snapshot = exhausted=%v remaining=%d, want true/0",
			s.RuntimeExhausted, s.RemainingRuntimeSeconds)
	}
}

// The no-regression half: unconfigured dimensions never report exhausted —
// a parent with no limit on a dimension keeps unlimited children. Cost
// without configured prices is never enforced, so it can never report
// exhausted either (matching the Remaining* computation's enforcement gate).
func TestCheckerSnapshot_UnconfiguredNotExhausted(t *testing.T) {
	c := NewChecker(Limits{MaxToolCalls: 5}, time.Now())
	s := c.Snapshot(0, 0)
	if s.RuntimeExhausted || s.InputTokensExhausted || s.OutputTokensExhausted || s.CostExhausted {
		t.Errorf("unconfigured dimensions reported exhausted: %+v", s)
	}
	if s.ToolCallsExhausted {
		t.Error("ToolCallsExhausted = true with 5/5 calls remaining, want false")
	}

	costOnly := NewChecker(Limits{MaxCostUSD: 1.0}, time.Now())
	s = costOnly.Snapshot(5000, 5000)
	if s.CostExhausted {
		t.Error("CostExhausted = true without configured prices, want false")
	}
}
