package loop

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestRevisionFailureCannotReuseEarlierPassingEvidence(t *testing.T) {
	create := acceptanceCall("p", "plan", `{"verb":"create","steps":[{"id":"verify","title":"Inspect output","checks":[{"id":"read","description":"Read current output","tool":"read_file","arguments":{"path":"output"}}]},{"id":"other","title":"Other work"}]}`)
	read := acceptanceCall("read", "read_file", `{"path":"output"}`)
	revision := acceptanceCall("rev", "plan", `{"verb":"revise","reason":"Prioritize other work","operations":[{"kind":"move","step_id":"verify","after_id":"other"}]}`)
	e, _ := acceptanceEngine(t, [][]session.ToolCall{{create}, {read}, {read, revision}, {acceptanceCall("done", "plan", `{"verb":"complete","step_id":"verify"}`)}}, false)
	var reads atomic.Int32
	e.registry = tool.NewRegistry([]tool.Tool{NewPlanTool(e.planStore), &contractTool{name: "read_file", run: func(string) (string, error) {
		if reads.Add(1) == 1 {
			return "old output", nil
		}
		return "", errors.New("output missing")
	}}})
	answer, history, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "Inspect output and adjust plan if needed."}})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := e.planStore.Snapshot()
	if state.Steps[0].ID != "other" {
		t.Fatal("revision did not execute")
	}
	if state.Steps[1].Status == StepDone || len(e.pendingPlanChecks()) != 1 {
		t.Fatalf("failed check retained earlier evidence across revision: %+v", state)
	}
	if !strings.Contains(answer, "[odek verification incomplete:") || history[len(history)-1].Content != answer {
		t.Fatal("missing persisted verification warning")
	}
}

func TestRevisionCannotClaimChecksFromItsOwnBatch(t *testing.T) {
	create := acceptanceCall("p", "plan", `{"verb":"create","steps":[{"id":"s","title":"Inspect output"}]}`)
	revise := acceptanceCall("r", "plan", `{"verb":"revise","reason":"Require verification","operations":[{"kind":"edit","step_id":"s","checks":[{"id":"inspect","description":"Read output","tool":"read_file","arguments":{"path":"output"}}]}]}`)
	read := acceptanceCall("read", "read_file", `{"path":"output"}`)
	for _, batch := range [][]session.ToolCall{{read, revise}, {revise, read}} {
		e, _ := acceptanceEngine(t, [][]session.ToolCall{{create}, batch, {acceptanceCall("done", "plan", `{"verb":"complete","step_id":"s"}`)}}, false)
		answer, err := e.Run(context.Background(), "Verify output")
		if err != nil {
			t.Fatal(err)
		}
		if len(e.pendingPlanChecks()) != 1 || !strings.Contains(answer, "[odek verification incomplete:") {
			t.Fatalf("same-batch call certified newly revised check: %s", answer)
		}
	}
}

func TestRevisionTriggersSkillRematchWithoutNewPlan(t *testing.T) {
	e := &Engine{}
	s := NewPlanStore(12, 4000)
	e.SetPlanStore(s)
	mustExecute(t, s, `{"verb":"create","steps":[{"id":"s","title":"Inspect"}]}`)
	e.skillRematchPending.Store(false)
	mustExecute(t, s, `{"verb":"revise","reason":"Found database dependency","operations":[{"kind":"add","steps":[{"id":"db","title":"Review database schema"}]}]}`)
	if !e.skillRematchPending.Load() {
		t.Fatal("revision did not request skill rematch")
	}
	if !strings.Contains(e.planTitleQuery(), "database") {
		t.Fatal("new title absent from skill query")
	}
}

func TestRevisionSurvivesWrappedTranscriptAndCompletion(t *testing.T) {
	e, _ := acceptanceEngine(t, [][]session.ToolCall{
		{acceptanceCall("p", "plan", `{"verb":"create","steps":[{"id":"s","title":"Inspect"}]}`)},
		{acceptanceCall("r", "plan", `{"verb":"revise","reason":"New evidence changes the approach","operations":[{"kind":"edit","step_id":"s","title":"Inspect configuration"}]}`)},
		{acceptanceCall("d", "plan", `{"verb":"complete","step_id":"s"}`)},
	}, false)
	e.SetUntrustedWrapper(func(source, content string) string {
		return "<untrusted_content_deadbeef source=\"" + source + "\">\n" + content + "\n</untrusted_content_deadbeef>"
	})
	_, history, err := e.RunWithMessages(context.Background(), []session.Message{{Role: "user", Content: "Inspect and adjust the plan."}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := ExtractPlan(history)
	if !ok || parsed.Revision == nil || parsed.Revision.Reason != "New evidence changes the approach" || len(parsed.Steps) != 1 || parsed.Steps[0].Status != StepDone {
		t.Fatalf("lost completed revised plan in wrapped transcript: %+v", parsed)
	}
	resumed, _ := acceptanceEngine(t, nil, false)
	_, _, err = resumed.RunWithMessages(context.Background(), append(history, session.Message{Role: "user", Content: "Report the plan."}))
	if err != nil {
		t.Fatal(err)
	}
	state, ok := resumed.planStore.Snapshot()
	if !ok || state.Revision == nil || len(state.Steps) != 1 {
		t.Fatalf("lost revision on resume: %+v", state)
	}
}
