package budget

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLimits_IsZero(t *testing.T) {
	if !(Limits{}).IsZero() {
		t.Error("empty Limits should be zero")
	}
	if (Limits{MaxToolCalls: 1}).IsZero() {
		t.Error("Limits with a set field should not be zero")
	}
}

func TestLimits_CostEnforcementActive(t *testing.T) {
	cases := []struct {
		name string
		l    Limits
		want bool
	}{
		{"nothing", Limits{}, false},
		{"cap only, no prices", Limits{MaxCostUSD: 1}, false},
		{"cap + input price only", Limits{MaxCostUSD: 1, InputCostPerMillionUSD: 2}, false},
		{"cap + output price only", Limits{MaxCostUSD: 1, OutputCostPerMillionUSD: 2}, false},
		{"prices only, no cap", Limits{InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 3}, false},
		{"cap + both prices", Limits{MaxCostUSD: 1, InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 3}, true},
	}
	for _, tc := range cases {
		if got := tc.l.CostEnforcementActive(); got != tc.want {
			t.Errorf("%s: CostEnforcementActive() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestError_Error(t *testing.T) {
	e := &Error{Limit: LimitInputTokens, Observed: 1200, Maximum: 1000}
	msg := e.Error()
	for _, want := range []string{"input_tokens", "1200", "1000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want substring %q", msg, want)
		}
	}

	cost := &Error{Limit: LimitCostUSD, Observed: 1_500_000, Maximum: 1_000_000}
	msg = cost.Error()
	if !strings.Contains(msg, "cost_usd") || !strings.Contains(msg, "$1.50") || !strings.Contains(msg, "$1.00") {
		t.Errorf("cost Error() = %q, want USD-formatted amounts", msg)
	}
}

func TestAs(t *testing.T) {
	berr := &Error{Limit: LimitRuntime, Observed: 10, Maximum: 5}
	if got, ok := As(berr); !ok || got != berr {
		t.Fatal("As should match a bare *Error")
	}
	wrapped := fmt.Errorf("iteration 3: %w", berr)
	if got, ok := As(wrapped); !ok || got != berr {
		t.Fatal("As should match a wrapped *Error")
	}
	if _, ok := As(errors.New("other")); ok {
		t.Fatal("As should not match an unrelated error")
	}
	if _, ok := As(nil); ok {
		t.Fatal("As should not match nil")
	}
}

func TestNewChecker_NoLimits(t *testing.T) {
	if c := NewChecker(Limits{}, time.Now()); c != nil {
		t.Fatal("NewChecker with zero limits should return nil")
	}
	// Nil checker is safe to call and never reports exhaustion.
	var c *Checker
	if c.CheckRuntime() != nil || c.CheckUsage(1<<40, 1<<40) != nil || c.CheckToolBatch(1<<20) != nil {
		t.Error("nil Checker should never report exhaustion")
	}
	c.RecordToolCalls(1) // must not panic
}

func TestChecker_Runtime(t *testing.T) {
	start := time.Now()
	now := start
	c := NewChecker(Limits{MaxRuntimeSeconds: 60}, start)
	c.SetNowFunc(func() time.Time { return now })

	if err := c.CheckRuntime(); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	now = now.Add(61 * time.Second)
	err := c.CheckRuntime()
	if err == nil {
		t.Fatal("expected runtime exhaustion")
	}
	if err.Limit != LimitRuntime || err.Maximum != 60 || err.Observed < 60 {
		t.Errorf("unexpected error: %+v", err)
	}
}

func TestChecker_Usage(t *testing.T) {
	c := NewChecker(Limits{MaxInputTokens: 1000, MaxOutputTokens: 500}, time.Now())
	if err := c.CheckUsage(999, 499); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	if err := c.CheckUsage(1000, 0); err == nil || err.Limit != LimitInputTokens {
		t.Fatalf("input token limit should fire at the cap, got %v", err)
	}
	if err := c.CheckUsage(0, 500); err == nil || err.Limit != LimitOutputTokens {
		t.Fatalf("output token limit should fire at the cap, got %v", err)
	}
}

func TestChecker_Cost(t *testing.T) {
	l := Limits{
		MaxCostUSD:              0.10,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 2.0,
	}
	c := NewChecker(l, time.Now())

	// 50k in ($0.05) + 20k out ($0.04) = $0.09 < $0.10
	if err := c.CheckUsage(50_000, 20_000); err != nil {
		t.Fatalf("within cost budget: %v", err)
	}
	// 60k in ($0.06) + 20k out ($0.04) = $0.10 >= cap
	err := c.CheckUsage(60_000, 20_000)
	if err == nil {
		t.Fatal("expected cost exhaustion")
	}
	if err.Limit != LimitCostUSD {
		t.Fatalf("limit = %q, want %q", err.Limit, LimitCostUSD)
	}
	if got := MicroToUSD(err.Observed); got < 0.099 || got > 0.101 {
		t.Errorf("observed = $%f, want ~$0.10", got)
	}
	if got := MicroToUSD(err.Maximum); got != 0.10 {
		t.Errorf("maximum = $%f, want $0.10", got)
	}

	// Without prices, the same cap is never enforced.
	noPrices := NewChecker(Limits{MaxCostUSD: 0.000001}, time.Now())
	if err := noPrices.CheckUsage(1<<40, 1<<40); err != nil {
		t.Fatalf("cost enforcement must stay disabled without prices: %v", err)
	}
}

func TestChecker_ToolCalls(t *testing.T) {
	c := NewChecker(Limits{MaxToolCalls: 3}, time.Now())

	if err := c.CheckToolBatch(3); err != nil {
		t.Fatalf("batch reaching exactly the cap should be allowed: %v", err)
	}
	c.RecordToolCalls(3)
	if err := c.CheckToolBatch(1); err == nil {
		t.Fatal("batch beyond the cap should be rejected")
	} else {
		if err.Limit != LimitToolCalls || err.Observed != 4 || err.Maximum != 3 {
			t.Errorf("unexpected error: %+v", err)
		}
	}
}

func TestLimits_ResolvePrices(t *testing.T) {
	flat := Limits{InputCostPerMillionUSD: 1.0, OutputCostPerMillionUSD: 2.0}
	withModels := Limits{
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 2.0,
		ModelPrices: map[string]ModelPrice{
			"model-a":    {InputCostPerMillionUSD: 0.5, OutputCostPerMillionUSD: 0.7},
			"model-b":    {InputCostPerMillionUSD: 0.5}, // output falls back
			"model-c":    {OutputCostPerMillionUSD: 0.7},
			"model-zero": {}, // both fall back
		},
	}
	cases := []struct {
		name    string
		l       Limits
		model   string
		wantIn  float64
		wantOut float64
	}{
		{"nil map falls back", flat, "model-a", 1.0, 2.0},
		{"empty map falls back", Limits{InputCostPerMillionUSD: 1.0, OutputCostPerMillionUSD: 2.0, ModelPrices: map[string]ModelPrice{}}, "model-a", 1.0, 2.0},
		{"exact match overrides both", withModels, "model-a", 0.5, 0.7},
		{"unknown model falls back", withModels, "model-x", 1.0, 2.0},
		{"no prefix matching", withModels, "model-a-v2", 1.0, 2.0},
		{"missing input falls back individually", withModels, "model-c", 1.0, 0.7},
		{"missing output falls back individually", withModels, "model-b", 0.5, 2.0},
		{"empty entry falls back entirely", withModels, "model-zero", 1.0, 2.0},
		{"empty model id falls back", withModels, "", 1.0, 2.0},
		{"no flat prices, match resolves", Limits{ModelPrices: map[string]ModelPrice{"m": {1.5, 2.5}}}, "m", 1.5, 2.5},
	}
	for _, tc := range cases {
		in, out := tc.l.ResolvePrices(tc.model)
		if in != tc.wantIn || out != tc.wantOut {
			t.Errorf("%s: ResolvePrices(%q) = (%v, %v), want (%v, %v)",
				tc.name, tc.model, in, out, tc.wantIn, tc.wantOut)
		}
	}
}

func TestLimits_ResolveForModel(t *testing.T) {
	l := Limits{
		MaxCostUSD:              1.0,
		InputCostPerMillionUSD:  1.0,
		OutputCostPerMillionUSD: 2.0,
		ModelPrices: map[string]ModelPrice{
			"model-a": {InputCostPerMillionUSD: 0.5},
		},
	}
	r := l.ResolveForModel("model-a")
	if r.InputCostPerMillionUSD != 0.5 || r.OutputCostPerMillionUSD != 2.0 {
		t.Errorf("resolved prices = (%v, %v), want (0.5, 2.0)", r.InputCostPerMillionUSD, r.OutputCostPerMillionUSD)
	}
	if !r.CostEnforcementActive() {
		t.Error("cost enforcement must be active on the resolved prices")
	}
	// The original is untouched.
	if l.InputCostPerMillionUSD != 1.0 || l.OutputCostPerMillionUSD != 2.0 {
		t.Errorf("ResolveForModel mutated the receiver: %+v", l)
	}
	// A model_prices-only entry can activate cost enforcement for its model
	// even when no flat prices are configured.
	onlyModel := Limits{MaxCostUSD: 1.0, ModelPrices: map[string]ModelPrice{"m": {3.0, 4.0}}}
	if onlyModel.CostEnforcementActive() {
		t.Error("unresolved limits with no flat prices should not report cost enforcement active")
	}
	if rm := onlyModel.ResolveForModel("m"); !rm.CostEnforcementActive() {
		t.Error("resolved limits for a priced model should report cost enforcement active")
	}
	if rm := onlyModel.ResolveForModel("other"); rm.CostEnforcementActive() {
		t.Error("resolved limits for an unpriced model should not report cost enforcement active")
	}
}

func TestLimits_IsZero_ModelPrices(t *testing.T) {
	if (Limits{ModelPrices: map[string]ModelPrice{"m": {1, 1}}}).IsZero() {
		t.Error("Limits with model_prices should not be zero")
	}
	if !(Limits{ModelPrices: map[string]ModelPrice{}}).IsZero() {
		t.Error("Limits with an empty model_prices map should be zero")
	}
}
