package config

import (
	"math"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
)

// FuzzClampProjectLimits is an invariant fuzz over the budget-limits merge:
// a project config must never raise, disable, or zero-out a global limit,
// and project-set prices must never survive the clamp. It feeds arbitrary
// global/project limit pairs into clampProjectLimits and checks the merge
// postconditions on the mutated project limits.
func FuzzClampProjectLimits(f *testing.F) {
	type seed struct {
		gRuntime, gTools, gIn, gOut int64
		gCost                       float64
		pRuntime, pTools, pIn, pOut int64
		pCost                       float64
		pInPrice, pOutPrice         float64
	}
	seeds := []seed{
		{60, 100, 1000, 2000, 1.5, 30, 50, 500, 1000, 1.0, 2.0, 3.0},    // project lowers everything
		{60, 100, 1000, 2000, 1.5, 120, 200, 5000, 9000, 3.0, 2.0, 3.0}, // project raises everything
		{60, 100, 1000, 2000, 1.5, 0, 0, 0, 0, 0, 0, 0},                 // project zeroes out
		{0, 0, 0, 0, 0, 30, 50, 500, 1000, 1.0, 2.0, 3.0},               // no global limits
		{60, 100, 1000, 2000, 1.5, -1, -1, -1, -1, -1, -1, -1},          // negative project values
		{-5, -5, -5, -5, -5, 10, 10, 10, 10, 10, 1, 1},                  // negative global values
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxFloat64, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxFloat64, 1, 1},
		{1, 1, 1, 1, 0.000001, 1, 1, 1, 1, 0.000001, 0.000001, 0.000001},
		{60, 100, 1000, 2000, 1.5, 60, 100, 1000, 2000, 1.5, 0, 0}, // exact match
		{0, 100, 0, 2000, 0, 30, 0, 500, 0, 1.0, 2.0, 3.0},         // mixed zero global
	}
	for _, s := range seeds {
		f.Add(s.gRuntime, s.gTools, s.gIn, s.gOut, s.gCost,
			s.pRuntime, s.pTools, s.pIn, s.pOut, s.pCost, s.pInPrice, s.pOutPrice)
	}

	f.Fuzz(func(t *testing.T, gRuntime, gTools, gIn, gOut int64, gCost float64,
		pRuntime, pTools, pIn, pOut int64, pCost, pInPrice, pOutPrice float64) {
		// Config files are JSON, which cannot encode NaN/Inf — skip
		// non-finite floats as unreachable input (CLI flags are
		// operator-controlled and can set limits explicitly either way).
		for _, v := range []float64{gCost, pCost, pInPrice, pOutPrice} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Skip("non-finite float: unreachable via JSON config")
			}
		}

		global := &budget.Limits{
			MaxRuntimeSeconds: gRuntime, MaxToolCalls: gTools,
			MaxInputTokens: gIn, MaxOutputTokens: gOut, MaxCostUSD: gCost,
			InputCostPerMillionUSD: 1.0, OutputCostPerMillionUSD: 2.0,
		}
		project := &budget.Limits{
			MaxRuntimeSeconds: pRuntime, MaxToolCalls: pTools,
			MaxInputTokens: pIn, MaxOutputTokens: pOut, MaxCostUSD: pCost,
			InputCostPerMillionUSD: pInPrice, OutputCostPerMillionUSD: pOutPrice,
		}

		clampProjectLimits(global, project)

		checkInt := func(name string, g, p int64) {
			if g <= 0 {
				if p != clampInputInt(p) { // global unset ⇒ project value passes through unchanged
					t.Fatalf("%s: global unset but project value %d mutated to %d", name, clampInputInt(p), p)
				}
				return
			}
			// Global limit set: project must end at a positive value ≤ global.
			if p <= 0 || p > g {
				t.Fatalf("%s: global=%d but clamped project=%d (must be in (0, %d])", name, g, p, g)
			}
		}
		checkFloat := func(name string, g, p float64) {
			if math.IsNaN(p) {
				t.Fatalf("%s: clamped project value is NaN (global=%g)", name, g)
			}
			if g <= 0 || math.IsNaN(g) {
				return // global unset/NaN ⇒ project value passes through
			}
			if p <= 0 || p > g {
				t.Fatalf("%s: global=%g but clamped project=%g (must be in (0, %g])", name, g, p, g)
			}
		}
		checkInt("max_runtime_seconds", gRuntime, project.MaxRuntimeSeconds)
		checkInt("max_tool_calls", gTools, project.MaxToolCalls)
		checkInt("max_input_tokens", gIn, project.MaxInputTokens)
		checkInt("max_output_tokens", gOut, project.MaxOutputTokens)
		checkFloat("max_cost_usd", gCost, project.MaxCostUSD)

		// Prices always come from the operator-controlled global config.
		if project.InputCostPerMillionUSD != global.InputCostPerMillionUSD ||
			project.OutputCostPerMillionUSD != global.OutputCostPerMillionUSD {
			t.Fatalf("project prices survived clamp: in=%g out=%g, want in=%g out=%g",
				project.InputCostPerMillionUSD, project.OutputCostPerMillionUSD,
				global.InputCostPerMillionUSD, global.OutputCostPerMillionUSD)
		}
	})
}

// clampInputInt documents the expected passthrough when no global limit is
// set; it exists only to make the fuzz assertion read clearly.
func clampInputInt(v int64) int64 { return v }
