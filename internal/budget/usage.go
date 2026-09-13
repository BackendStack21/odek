package budget

import "math"

// Usage carries provider-reported work without collapsing output, cache,
// or differently priced child-model usage into parent input tokens.
type Usage struct {
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	ToolCalls           int64   `json:"tool_calls"`
	CostUSD             float64 `json:"cost_usd"`
	CostKnown           bool    `json:"cost_known"`
}

func (u Usage) Valid() bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CacheReadTokens >= 0 &&
		u.CacheCreationTokens >= 0 && u.ToolCalls >= 0 && u.CostUSD >= 0 &&
		!math.IsNaN(u.CostUSD) && !math.IsInf(u.CostUSD, 0)
}

func (u Usage) TotalInput() int64 {
	return AddCount(AddCount(u.InputTokens, u.CacheReadTokens), u.CacheCreationTokens)
}

// AddCount preserves exhausted counters even for oversized reported usage.
func AddCount(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// Grant is an immutable, single-settlement reservation owned by the engine.
// Zero limit fields remain unconfigured; an exhausted configured dimension
// is rejected before a grant is created.
type Grant struct {
	ID     uint64
	Limits Limits
}

// RecordExternal records tools and the difference between a child's own
// price and the parent's price. Token totals are maintained by the engine.
func (c *Checker) RecordExternal(u Usage) {
	if c == nil {
		return
	}
	c.toolCalls = AddCount(c.toolCalls, u.ToolCalls)
	if u.CostKnown {
		c.externalCostAdjustment += u.CostUSD - c.limits.EstimatedCostUSD(u.TotalInput(), u.OutputTokens)
	}
}

func (c *Checker) Cost(inputTokens, outputTokens int64) float64 {
	if c == nil {
		return 0
	}
	return math.Max(0, c.limits.EstimatedCostUSD(inputTokens, outputTokens)+c.externalCostAdjustment)
}

func (c *Checker) ToolCalls() int64 {
	if c == nil {
		return 0
	}
	return c.toolCalls
}
