// Package budget implements hard execution budgets for an agent run
// (odek-extension/v1 — see docs/EXTENSIONS.md): a Limits struct describing
// the configured caps, a typed Error returned when a cap is exhausted, and a
// Checker the loop engine consults at each enforcement point.
//
// Budgets are hard stops, not hints: once a limit is exhausted the loop
// emits a budget_exceeded event, persists the latest safe session state, and
// returns a typed *Error so callers (CLI dispatch, orchestrators) can tell a
// budget stop apart from a model or tool failure.
package budget

import (
	"errors"
	"fmt"
	"time"
)

// Limit names carried in Error.Limit and in budget_exceeded events
// (data.limit_name). These strings are part of the odek-extension/v1
// contract and must match the events.Limit* constants.
const (
	LimitRuntime      = "runtime"
	LimitToolCalls    = "tool_calls"
	LimitInputTokens  = "input_tokens"
	LimitOutputTokens = "output_tokens"
	LimitCostUSD      = "cost_usd"
)

// Limits describes the hard execution budgets for a single agent run.
// All fields are optional: zero means "no limit" (or, for prices, "not
// configured"). Values come from the operator config layers — never from
// the LLM and never hard-coded per provider.
type Limits struct {
	// MaxRuntimeSeconds caps the wall-clock duration of a run.
	MaxRuntimeSeconds int64 `json:"max_runtime_seconds,omitempty"`

	// MaxToolCalls caps the total number of tool calls executed.
	MaxToolCalls int64 `json:"max_tool_calls,omitempty"`

	// MaxInputTokens caps cumulative prompt tokens across LLM calls.
	MaxInputTokens int64 `json:"max_input_tokens,omitempty"`

	// MaxOutputTokens caps cumulative completion tokens across LLM calls.
	MaxOutputTokens int64 `json:"max_output_tokens,omitempty"`

	// MaxCostUSD caps the estimated spend of a run. Enforcement is active
	// only when both resolved per-million prices (see ResolvePrices) are
	// configured too.
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`

	// InputCostPerMillionUSD is the operator-configured price of one million
	// input tokens. odek never hard-codes provider prices.
	InputCostPerMillionUSD float64 `json:"input_cost_per_million_usd,omitempty"`

	// OutputCostPerMillionUSD is the operator-configured price of one million
	// output tokens. odek never hard-codes provider prices.
	OutputCostPerMillionUSD float64 `json:"output_cost_per_million_usd,omitempty"`

	// ModelPrices maps exact model IDs to per-model token prices. When the
	// run's model ID matches a key exactly, that entry's prices override the
	// flat pair above (per field — a missing price in the entry falls back
	// to the flat value individually). See ResolvePrices.
	ModelPrices map[string]ModelPrice `json:"model_prices,omitempty"`
}

// ModelPrice is the per-model token price entry in Limits.ModelPrices.
// A zero field means "fall back to the flat Limits price for that field".
type ModelPrice struct {
	InputCostPerMillionUSD  float64 `json:"input_cost_per_million_usd,omitempty"`
	OutputCostPerMillionUSD float64 `json:"output_cost_per_million_usd,omitempty"`
}

// IsZero reports whether no limit is configured at all.
func (l Limits) IsZero() bool {
	return l.MaxRuntimeSeconds == 0 && l.MaxToolCalls == 0 &&
		l.MaxInputTokens == 0 && l.MaxOutputTokens == 0 &&
		l.MaxCostUSD == 0 && l.InputCostPerMillionUSD == 0 &&
		l.OutputCostPerMillionUSD == 0 && len(l.ModelPrices) == 0
}

// ResolvePrices returns the effective per-million input/output prices for
// the given model ID: an exact ModelPrices key match overrides the flat
// pair, with each missing (non-positive) price in the entry falling back
// to the flat value individually. No normalization or prefix matching.
func (l Limits) ResolvePrices(model string) (inputPerMillion, outputPerMillion float64) {
	inputPerMillion, outputPerMillion = l.InputCostPerMillionUSD, l.OutputCostPerMillionUSD
	if p, ok := l.ModelPrices[model]; ok {
		if p.InputCostPerMillionUSD > 0 {
			inputPerMillion = p.InputCostPerMillionUSD
		}
		if p.OutputCostPerMillionUSD > 0 {
			outputPerMillion = p.OutputCostPerMillionUSD
		}
	}
	return inputPerMillion, outputPerMillion
}

// ResolveForModel returns a copy of l with the flat prices replaced by the
// prices resolved for the given model ID (see ResolvePrices). The model is
// fixed per run, so callers resolve once at engine/checker setup; every
// downstream cost check (CostEnforcementActive, EstimatedCostUSD) then uses
// the effective prices unchanged.
func (l Limits) ResolveForModel(model string) Limits {
	l.InputCostPerMillionUSD, l.OutputCostPerMillionUSD = l.ResolvePrices(model)
	return l
}

// CostEnforcementActive reports whether cost enforcement is in effect: the
// cost cap is set AND both per-million prices are configured. Without prices
// there is no way to estimate spend — odek never guesses provider prices —
// so the token budgets stay active while cost checks are disabled.
func (l Limits) CostEnforcementActive() bool {
	return l.MaxCostUSD > 0 && l.InputCostPerMillionUSD > 0 && l.OutputCostPerMillionUSD > 0
}

// EstimatedCostUSD returns the estimated spend for the given cumulative
// token totals, using the configured per-million prices.
func (l Limits) EstimatedCostUSD(inputTokens, outputTokens int64) float64 {
	return float64(inputTokens)/1e6*l.InputCostPerMillionUSD +
		float64(outputTokens)/1e6*l.OutputCostPerMillionUSD
}

// Error is the typed error returned when an execution budget is exhausted.
// Limit is one of the Limit* constants. Observed and Maximum are counts in
// the limit's natural unit (seconds, tool calls, tokens) — except for
// LimitCostUSD, where both are micro-USD (1e-6 USD) so the fields stay
// int64; use MicroToUSD to convert for display.
type Error struct {
	Limit    string
	Observed int64
	Maximum  int64
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Limit == LimitCostUSD {
		return fmt.Sprintf("execution budget exceeded: %s observed $%.6f (limit $%.6f)",
			e.Limit, MicroToUSD(e.Observed), MicroToUSD(e.Maximum))
	}
	return fmt.Sprintf("execution budget exceeded: %s observed %d (limit %d)",
		e.Limit, e.Observed, e.Maximum)
}

// As reports whether err (or any error it wraps) is a budget exhaustion
// error, returning the typed *Error when so. It is the canonical way for
// callers to map a run failure to the budget exit path (CLI exit code 4).
func As(err error) (*Error, bool) {
	var berr *Error
	if errors.As(err, &berr) {
		return berr, true
	}
	return nil, false
}

// microUSD converts a USD amount to micro-USD for Error's int64 fields.
func microUSD(usd float64) int64 { return int64(usd * 1e6) }

// MicroToUSD converts a micro-USD value from an Error back to USD.
func MicroToUSD(micro int64) float64 { return float64(micro) / 1e6 }

// Checker tracks consumption against a Limits set for a single run. The
// zero/nil Checker is safe to call and never reports exhaustion, so call
// sites need no nil guards beyond NewChecker returning nil for empty limits.
//
// Checker is not safe for concurrent use; the loop engine consults it from
// the single loop goroutine only.
type Checker struct {
	limits    Limits
	start     time.Time
	now       func() time.Time
	toolCalls int64
}

// NewChecker returns a Checker enforcing l, measuring runtime from start.
// It returns nil when l configures no limits at all, letting callers skip
// every check cheaply.
func NewChecker(l Limits, start time.Time) *Checker {
	if l.IsZero() {
		return nil
	}
	return &Checker{limits: l, start: start, now: time.Now}
}

// SetNowFunc overrides the wall clock used for the runtime limit.
// Intended for tests.
func (c *Checker) SetNowFunc(fn func() time.Time) {
	if c != nil && fn != nil {
		c.now = fn
	}
}

// Limits returns the limits the checker enforces.
func (c *Checker) Limits() Limits {
	if c == nil {
		return Limits{}
	}
	return c.limits
}

// CheckRuntime reports exhaustion of the wall-clock budget. Nil-safe.
func (c *Checker) CheckRuntime() *Error {
	if c == nil || c.limits.MaxRuntimeSeconds <= 0 {
		return nil
	}
	elapsed := int64(c.now().Sub(c.start).Seconds())
	if elapsed >= c.limits.MaxRuntimeSeconds {
		return &Error{Limit: LimitRuntime, Observed: elapsed, Maximum: c.limits.MaxRuntimeSeconds}
	}
	return nil
}

// CheckUsage reports exhaustion of the token budgets and (when prices are
// configured) the estimated-cost budget, given the cumulative token totals.
// Nil-safe.
func (c *Checker) CheckUsage(inputTokens, outputTokens int64) *Error {
	if c == nil {
		return nil
	}
	if max := c.limits.MaxInputTokens; max > 0 && inputTokens >= max {
		return &Error{Limit: LimitInputTokens, Observed: inputTokens, Maximum: max}
	}
	if max := c.limits.MaxOutputTokens; max > 0 && outputTokens >= max {
		return &Error{Limit: LimitOutputTokens, Observed: outputTokens, Maximum: max}
	}
	if c.limits.CostEnforcementActive() {
		cost := c.limits.EstimatedCostUSD(inputTokens, outputTokens)
		if cost >= c.limits.MaxCostUSD {
			return &Error{Limit: LimitCostUSD, Observed: microUSD(cost), Maximum: microUSD(c.limits.MaxCostUSD)}
		}
	}
	return nil
}

// CheckToolBatch reports whether executing a batch of n further tool calls
// would exceed the tool-call budget. It is consulted BEFORE the batch is
// scheduled; on exhaustion no new tool work starts. Observed is the
// would-be total (already executed + this batch). Nil-safe.
func (c *Checker) CheckToolBatch(n int) *Error {
	if c == nil || c.limits.MaxToolCalls <= 0 {
		return nil
	}
	wouldBe := c.toolCalls + int64(n)
	if wouldBe > c.limits.MaxToolCalls {
		return &Error{Limit: LimitToolCalls, Observed: wouldBe, Maximum: c.limits.MaxToolCalls}
	}
	return nil
}

// RecordToolCalls accounts n executed tool calls against the budget.
func (c *Checker) RecordToolCalls(n int) {
	if c != nil {
		c.toolCalls += int64(n)
	}
}
