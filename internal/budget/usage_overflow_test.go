package budget

import (
	"math"
	"testing"
)

// Provider-reported costs far beyond int64 range (or non-finite) leaked into
// externalCostAdjustment unclamped; microUSD conversion of such values is
// platform-dependent (saturation vs negative wrap) and poisoned Observed.
func TestRecordExternal_OverflowingCostClamped(t *testing.T) {
	c := &Checker{limits: Limits{
		MaxInputTokens:          1_000_000,
		MaxOutputTokens:         1_000_000,
		MaxCostUSD:              100,
		InputCostPerMillionUSD:  1,
		OutputCostPerMillionUSD: 1,
	}}
	c.RecordExternal(Usage{CostKnown: true, CostUSD: 1e19})
	c.RecordExternal(Usage{CostKnown: true, CostUSD: math.Inf(1)})
	c.RecordExternal(Usage{CostKnown: true, CostUSD: math.NaN()})

	cost := c.Cost(10, 10)
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		t.Fatalf("Cost returned non-finite value %v after overflowing external usage", cost)
	}
	if cost > 1e12 {
		t.Fatalf("Cost %v exceeds clamped ceiling", cost)
	}
	if err := c.CheckUsage(10, 10); err != nil && err.Observed < 0 {
		t.Fatalf("CheckUsage reported negative Observed %d", err.Observed)
	}
}
