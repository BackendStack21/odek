package eval

import (
	"context"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
)

func TestDefaultScenariosAreIndependentAndBounded(t *testing.T) {
	r := Run(context.Background(), Scenarios())
	if r.Total != 11 {
		t.Fatalf("total=%d want 11", r.Total)
	}
	if r.Failed != 0 {
		t.Fatalf("scenario failures=%d report=%+v", r.Failed, r)
	}
	if !r.TokensKnown || r.CostKnown {
		t.Fatalf("token/cost availability = %v/%v", r.TokensKnown, r.CostKnown)
	}
	if r.FalseCompletionRate <= 0 {
		t.Fatalf("false completion rate=%v", r.FalseCompletionRate)
	}
	var taskSuccess int
	for _, c := range r.Cases {
		if c.TaskSuccess {
			taskSuccess++
		}
	}
	if taskSuccess != 4 {
		t.Fatalf("task successes=%d want 4", taskSuccess)
	}
	for _, c := range r.Cases {
		if c.Name == "plan_check_failed" || c.Name == "plan_check_missing" {
			if c.FalseCompletion {
				t.Errorf("%s treated guarded incomplete result as false completion", c.Name)
			}
		}
	}
}

func TestHarnessRejectsMissingOracleOrClient(t *testing.T) {
	missing := Run(context.Background(), []Scenario{{Name: "no oracle"}})
	if missing.Failed != 1 || missing.Cases[0].Error == "" {
		t.Fatalf("accepted no oracle: %+v", missing)
	}
	emptyClient := RunWithOptions(context.Background(), []Scenario{{Name: "no client", Oracle: func(*Fixture, string, error, []ToolCall) OracleResult { return OracleResult{} }}}, RunOptions{ClientFactory: func(string) (*llmclient.Client, error) { return nil, nil }})
	if emptyClient.Failed != 1 || emptyClient.Cases[0].Error == "" {
		t.Fatalf("accepted no client: %+v", emptyClient)
	}
}
