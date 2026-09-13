package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
)

type recordingBudgetOwner struct {
	grant   budget.Grant
	usage   *budget.Usage
	settled int
	divisor int
}

func (o *recordingBudgetOwner) BudgetSnapshot() budget.Snapshot { return budget.Snapshot{} }
func (o *recordingBudgetOwner) ReserveExternalBudget(d int) (budget.Grant, error) {
	o.divisor = d
	return o.grant, nil
}
func (o *recordingBudgetOwner) SettleExternalBudget(_ budget.Grant, u *budget.Usage) {
	o.usage = u
	o.settled++
}

func TestChildBudgetSettlementAcrossProcessOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, script      string
		started, reported bool
	}{
		{"start failure", "", false, false},
		{"crash", "#!/bin/sh\nexit 1\n", true, false},
		{"legacy result", "#!/bin/sh\nprintf '%s\\n' '{" + `"status":"success","summary":"ok","tokens_used":7` + "}'\n", true, false},
		{"provider usage", "#!/bin/sh\nprintf '%s\\n' '{" + `"status":"success","summary":"ok","tokens_used":999,"usage":{"input_tokens":20,"output_tokens":10,"cache_read_tokens":3,"cache_creation_tokens":2,"tool_calls":4,"cost_usd":0.2,"cost_known":true}` + "}'\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "child")
			if tc.script != "" {
				if err := os.WriteFile(path, []byte(tc.script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			o := &recordingBudgetOwner{grant: budget.Grant{ID: 1, Limits: budget.Limits{MaxInputTokens: 100, MaxOutputTokens: 50, MaxToolCalls: 8, MaxCostUSD: 1}}}
			tool := &delegateTasksTool{odekPath: path, timeout: 3 * time.Second, maxConcurrency: 2, budgetInherit: config.BudgetInheritShare, budgetView: o}
			tool.runTask(0, "budget-child", "test", "", "", "", "", "", "")
			if o.settled != 1 || o.divisor != 3 {
				t.Fatalf("settlement count/share: %+v", o)
			}
			if !tc.started {
				if o.usage == nil || o.usage.TotalInput() != 0 {
					t.Fatalf("unstarted child charged: %+v", o.usage)
				}
			} else if !tc.reported {
				if o.usage != nil {
					t.Fatalf("unreported work refunded: %+v", o.usage)
				}
			} else if o.usage == nil || o.usage.TotalInput() != 25 || o.usage.OutputTokens != 10 || o.usage.ToolCalls != 4 || o.usage.CostUSD != .2 {
				t.Fatalf("wrong provider vector: %+v", o.usage)
			}
		})
	}
}

func TestOutputOnlyBudgetPassdownAndExhaustion(t *testing.T) {
	tb := taskBudgetFromSnapshot(budget.Snapshot{MaxOutputTokens: 100, RemainingOutputTokens: 40})
	if tb == nil || clampLimits(budget.Limits{}, tb).MaxOutputTokens != 40 {
		t.Fatalf("output cap lost: %+v", tb)
	}
	tb = taskBudgetFromSnapshot(budget.Snapshot{MaxOutputTokens: 100, OutputTokensExhausted: true})
	if err := exhaustedTaskBudget(tb); err == nil || err.Limit != budget.LimitOutputTokens {
		t.Fatalf("output exhaustion lost: %v", err)
	}
}
