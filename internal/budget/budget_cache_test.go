package budget

import (
	"testing"
	"time"
)

// Bug-sweep 2026-08-31: cache tokens (Anthropic cache_creation/cache_read,
// OpenAI cached_tokens, DeepSeek hit/miss) are real prompt tokens with real
// cost, but CheckUsage never saw them — a cache-heavy run could blow
// max_input_tokens and max_cost_usd without ever tripping the checker.
// CheckUsageWithCache is the enforcement entry the loop must use.

func TestCheckUsageWithCache_CountsCacheTokensTowardInputCap(t *testing.T) {
	c := NewChecker(Limits{MaxInputTokens: 1000}, time.Now())
	if err := c.CheckUsage(900, 0); err != nil {
		t.Fatalf("uncached 900 under cap 1000 should pass, got %v", err)
	}
	err := c.CheckUsageWithCache(900, 150, 50, 0) // 1100 total input
	if err == nil || err.Limit != LimitInputTokens {
		t.Fatalf("CheckUsageWithCache(900, 150, 50, 0) = %v, want LimitInputTokens", err)
	}
}

func TestCheckUsageWithCache_CountsCacheTokensTowardCostCap(t *testing.T) {
	l := Limits{
		MaxCostUSD:              1.0,
		InputCostPerMillionUSD:  2.0,
		OutputCostPerMillionUSD: 1.0, // CostEnforcementActive requires both prices
	}
	c := NewChecker(l, time.Now())
	if err := c.CheckUsage(0, 0); err != nil {
		t.Fatalf("no usage should pass, got %v", err)
	}
	// 600k cache tokens at $2/M = $1.20 ≥ $1.00 cap.
	err := c.CheckUsageWithCache(0, 600_000, 0, 0)
	if err == nil || err.Limit != LimitCostUSD {
		t.Fatalf("CheckUsageWithCache(0, 600k, 0, 0) = %v, want LimitCostUSD", err)
	}
}

func TestCheckUsageWithCache_UnderCapPasses(t *testing.T) {
	c := NewChecker(Limits{MaxInputTokens: 10_000}, time.Now())
	if err := c.CheckUsageWithCache(900, 150, 50, 20); err != nil {
		t.Fatalf("1120 input under cap 10k should pass, got %v", err)
	}
}

func TestCheckUsageWithCache_NilChecker(t *testing.T) {
	var c *Checker
	if err := c.CheckUsageWithCache(1<<40, 1<<40, 1<<40, 1<<40); err != nil {
		t.Fatalf("nil checker must never report exhaustion, got %v", err)
	}
}
