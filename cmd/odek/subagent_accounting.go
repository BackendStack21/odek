package main

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
)

type childBudgetReservation struct {
	limits       *taskBudget
	started      bool
	usage        *budget.Usage
	legacyTokens int64
	finish       func(*childBudgetReservation)
}

func (r *childBudgetReservation) settle() {
	if r.finish != nil {
		r.finish(r)
	}
}

func (r *childBudgetReservation) record(result map[string]any) {
	if raw, ok := result["usage"]; ok {
		data, err := json.Marshal(raw)
		var u *budget.Usage
		if err == nil && json.Unmarshal(data, &u) == nil && u != nil && u.Valid() {
			r.usage = u
		}
	}
	if n, ok := result["tokens_used"].(float64); ok && n > 0 && n < float64(math.MaxInt64) {
		r.legacyTokens = int64(n)
	}
}

func (t *delegateTasksTool) reserveChildBudget() (*childBudgetReservation, error) {
	r := &childBudgetReservation{}
	if t.budgetInherit != config.BudgetInheritShare {
		return r, nil
	}
	t.budgetMu.Lock()
	v := t.budgetView
	t.budgetMu.Unlock()
	if v == nil {
		return r, nil
	}
	if owner, ok := v.(interface {
		ReserveExternalBudget(int) (budget.Grant, error)
		SettleExternalBudget(budget.Grant, *budget.Usage)
	}); ok {
		grant, err := owner.ReserveExternalBudget(cap(t.concurrencySem()) + 1)
		if err != nil {
			return r, fmt.Errorf("subagent not spawned: %w", err)
		}
		l := grant.Limits
		r.limits = &taskBudget{MaxRuntimeSeconds: l.MaxRuntimeSeconds, MaxToolCalls: l.MaxToolCalls,
			MaxInputTokens: l.MaxInputTokens, MaxOutputTokens: l.MaxOutputTokens, MaxCostUSD: l.MaxCostUSD}
		r.finish = func(r *childBudgetReservation) {
			u := r.usage
			if !r.started {
				u = &budget.Usage{CostKnown: true}
			}
			owner.SettleExternalBudget(grant, u)
		}
		return r, nil
	}
	// Compatibility for embedding hosts exposing the original input-only hook.
	// The built-in engine always uses the complete vector above.
	s := v.BudgetSnapshot()
	r.limits = taskBudgetFromSnapshot(s)
	if err := exhaustedTaskBudget(r.limits); err != nil {
		return r, fmt.Errorf("subagent not spawned: %w", err)
	}
	if owner, ok := v.(interface {
		ReserveExternalUsage(int64) int64
		SettleExternalUsage(int64, int64)
	}); ok && s.MaxInputTokens > 0 {
		grant := owner.ReserveExternalUsage(s.RemainingInputTokens)
		if grant <= 0 {
			return r, fmt.Errorf("subagent not spawned: input-token budget fully committed to in-flight sub-agents")
		}
		r.limits.MaxInputTokens = grant
		r.finish = func(r *childBudgetReservation) {
			n := r.legacyTokens
			if r.started && n == 0 {
				n = grant
			}
			owner.SettleExternalUsage(grant, n)
		}
	} else if owner, ok := v.(interface{ ChargeExternalUsage(int64) }); ok {
		r.finish = func(r *childBudgetReservation) { owner.ChargeExternalUsage(r.legacyTokens) }
	}
	return r, nil
}
