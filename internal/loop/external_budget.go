package loop

import (
	"github.com/BackendStack21/odek/internal/budget"
)

func (e *Engine) budgetSnapshotLocked() budget.Snapshot {
	s := e.budget.Snapshot(budget.AddCount(budget.AddCount(int64(e.TotalInputTokens), int64(e.TotalCacheReadTokens)), int64(e.TotalCacheCreationTokens)), int64(e.TotalOutputTokens))
	for _, g := range e.externalGrants {
		s.RemainingInputTokens = max(0, s.RemainingInputTokens-g.Limits.MaxInputTokens)
		s.RemainingOutputTokens = max(0, s.RemainingOutputTokens-g.Limits.MaxOutputTokens)
		s.RemainingToolCalls = max(0, s.RemainingToolCalls-g.Limits.MaxToolCalls)
		s.RemainingCostUSD = max(0, s.RemainingCostUSD-g.Limits.MaxCostUSD)
	}
	s.InputTokensExhausted = s.MaxInputTokens > 0 && s.RemainingInputTokens == 0
	s.OutputTokensExhausted = s.MaxOutputTokens > 0 && s.RemainingOutputTokens == 0
	s.ToolCallsExhausted = s.MaxToolCalls > 0 && s.RemainingToolCalls == 0
	if e.budget.Limits().CostEnforcementActive() {
		s.CostExhausted = s.RemainingCostUSD <= 0
	}
	return s
}

// ReserveExternalBudget shares each configured dimension while leaving room
// for sibling work and the parent's final synthesis. Runtime is a shared
// deadline, not a quantity consumed independently by parallel children.
func (e *Engine) ReserveExternalBudget(divisor int) (budget.Grant, error) {
	return e.reserveExternalBudget(divisor, true)
}

// ReserveInferenceBudget reserves model-call headroom without reserving tool
// calls: the enclosing vision tool has already been counted by the loop.
func (e *Engine) ReserveInferenceBudget() (budget.Grant, error) {
	return e.reserveExternalBudget(2, false)
}

func (e *Engine) reserveExternalBudget(divisor int, includeTools bool) (budget.Grant, error) {
	e.externalChargeMu.Lock()
	defer e.externalChargeMu.Unlock()
	s := e.budgetSnapshotLocked()
	s.RemainingInputTokens = max(0, s.RemainingInputTokens-e.externalReserved)
	d := int64(max(2, divisor))
	l := budget.Limits{MaxRuntimeSeconds: s.RemainingRuntimeSeconds}
	if s.RuntimeExhausted {
		return budget.Grant{}, &budget.Error{Limit: budget.LimitRuntime}
	}
	for _, dim := range []struct {
		max, remaining int64
		target         *int64
		name           string
	}{
		{s.MaxInputTokens, s.RemainingInputTokens, &l.MaxInputTokens, budget.LimitInputTokens},
		{s.MaxOutputTokens, s.RemainingOutputTokens, &l.MaxOutputTokens, budget.LimitOutputTokens},
		{s.MaxToolCalls, s.RemainingToolCalls, &l.MaxToolCalls, budget.LimitToolCalls},
	} {
		if !includeTools && dim.name == budget.LimitToolCalls {
			continue
		}
		if dim.max > 0 {
			*dim.target = dim.remaining / d
			if *dim.target == 0 {
				return budget.Grant{}, &budget.Error{Limit: dim.name, Maximum: dim.max}
			}
		}
	}
	if e.budget.Limits().CostEnforcementActive() {
		l.MaxCostUSD = s.RemainingCostUSD / float64(d)
		if l.MaxCostUSD <= 0 {
			return budget.Grant{}, &budget.Error{Limit: budget.LimitCostUSD}
		}
	}
	e.externalGrantSeq++
	g := budget.Grant{ID: e.externalGrantSeq, Limits: l}
	if e.externalGrants == nil {
		e.externalGrants = make(map[uint64]budget.Grant)
	}
	e.externalGrants[g.ID] = g
	return g, nil
}

// SettleExternalBudget releases a reservation exactly once and records all
// actual usage, including overshoot. A missing/invalid final report consumes
// the grant conservatively; it never refunds a possibly executed child.
func (e *Engine) SettleExternalBudget(grant budget.Grant, usage *budget.Usage) {
	e.externalChargeMu.Lock()
	defer e.externalChargeMu.Unlock()
	g, ok := e.externalGrants[grant.ID]
	if !ok {
		return
	}
	delete(e.externalGrants, grant.ID)
	u := budget.Usage{InputTokens: g.Limits.MaxInputTokens, OutputTokens: g.Limits.MaxOutputTokens,
		ToolCalls: g.Limits.MaxToolCalls, CostUSD: g.Limits.MaxCostUSD, CostKnown: g.Limits.MaxCostUSD > 0}
	if usage != nil && usage.Valid() {
		u = *usage
		if g.Limits.MaxCostUSD > 0 && !u.CostKnown {
			u.CostUSD, u.CostKnown = g.Limits.MaxCostUSD, true
		}
	}
	e.TotalInputTokens = int(budget.AddCount(int64(e.TotalInputTokens), u.InputTokens))
	e.TotalOutputTokens = int(budget.AddCount(int64(e.TotalOutputTokens), u.OutputTokens))
	e.TotalCacheReadTokens = int(budget.AddCount(int64(e.TotalCacheReadTokens), u.CacheReadTokens))
	e.TotalCacheCreationTokens = int(budget.AddCount(int64(e.TotalCacheCreationTokens), u.CacheCreationTokens))
	e.budget.RecordExternal(u)
}

// BudgetUsage is the complete cumulative usage of this run and its children.
// Callers read it between steps or after Run; sibling tools may read it while
// the loop is blocked on their batch.
func (e *Engine) BudgetUsage() budget.Usage {
	e.externalChargeMu.Lock()
	defer e.externalChargeMu.Unlock()
	u := budget.Usage{InputTokens: int64(e.TotalInputTokens), OutputTokens: int64(e.TotalOutputTokens),
		CacheReadTokens: int64(e.TotalCacheReadTokens), CacheCreationTokens: int64(e.TotalCacheCreationTokens),
		ToolCalls: e.budget.ToolCalls(), CostKnown: e.budget.Limits().InputCostPerMillionUSD > 0 && e.budget.Limits().OutputCostPerMillionUSD > 0}
	u.CostUSD = e.budget.Cost(u.TotalInput(), u.OutputTokens)
	return u
}
