package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestRegression_ModelSwitchUpdatesCostBudget(t *testing.T) {
	var requestedModel atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requestedModel.Store(body.Model)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, budgetFinalResponse("done", 1000, 1000))
	}))
	defer server.Close()
	e := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 3, "", nil, 0)
	e.SetLimits(budget.Limits{MaxCostUSD: .01, ModelPrices: map[string]budget.ModelPrice{
		"test-model":      {InputCostPerMillionUSD: 1, OutputCostPerMillionUSD: 1},
		"expensive-model": {InputCostPerMillionUSD: 100, OutputCostPerMillionUSD: 100},
	}}, "test-model")
	e.SetModel("expensive-model")
	_, err := e.Run(context.Background(), "hello")
	if requestedModel.Load() != "expensive-model" {
		t.Fatalf("model switch did not reach provider: %v", requestedModel.Load())
	}
	if be, ok := budget.As(err); !ok || be.Limit != budget.LimitCostUSD {
		t.Fatalf("$0.20 response passed $0.01 cap after model switch: err=%v cost=%g prices=%+v", err, e.BudgetUsage().CostUSD, e.budgetLimits)
	}
}

func TestModelSwitchUsesOriginalFallbackPricesAcrossRuns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, budgetFinalResponse("done", 1000, 1000))
	}))
	defer server.Close()
	e := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 3, "", nil, 0)
	e.SetLimits(budget.Limits{MaxCostUSD: 1, InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 3, ModelPrices: map[string]budget.ModelPrice{
		"test-model":           {InputCostPerMillionUSD: 1, OutputCostPerMillionUSD: 1},
		"expensive-model":      {InputCostPerMillionUSD: 100, OutputCostPerMillionUSD: 200},
		"partial-prices-model": {InputCostPerMillionUSD: 4},
	}}, "test-model")
	for _, tc := range []struct {
		model string
		cost  float64
	}{
		{"expensive-model", .3}, {"partial-prices-model", .007}, {"unlisted-model", .005}, {"test-model", .002},
	} {
		e.SetModel(tc.model)
		if _, err := e.Run(t.Context(), "hello"); err != nil {
			t.Fatal(err)
		}
		if got := e.BudgetUsage().CostUSD; math.Abs(got-tc.cost) > 1e-12 {
			t.Errorf("%s cost=%g want=%g", tc.model, got, tc.cost)
		}
	}
}
