package eval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
)

func TestDefaultScenariosAreIndependentAndBounded(t *testing.T) {
	r := Run(context.Background(), Scenarios())
	if r.Total != 14 {
		t.Fatalf("total=%d want 14", r.Total)
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
	if taskSuccess != 7 {
		t.Fatalf("task successes=%d want 7", taskSuccess)
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

func TestScriptedResponsesEscapeJSONStrings(t *testing.T) {
	value := "quoted\"\\\n\x01"
	args := `{"value":"quoted\""}`
	var response struct {
		Choices []struct {
			Message struct {
				Content   string
				ToolCalls []struct {
					ID       string
					Function struct {
						Name      string
						Arguments string
					}
				} `json:"tool_calls"`
			}
		}
	}
	if err := json.Unmarshal([]byte(toolCall(value, value, args)), &response); err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != value || call.Function.Name != value || call.Function.Arguments != args {
		t.Fatal("tool fixture strings changed during encoding")
	}
	if err := json.Unmarshal([]byte(final(value)), &response); err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != value {
		t.Fatal("final fixture text changed during encoding")
	}
}
