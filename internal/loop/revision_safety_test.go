package loop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRevisionMalformedChangesAreAtomic(t *testing.T) {
	for name, op := range map[string]string{
		"duplicate check identity": `{"kind":"edit","step_id":"s1","checks":[{"id":"c1","description":"Different requirement","tool":"read_file","arguments":{"path":"elsewhere"}}]}`,
		"empty addition":           `{"kind":"add","steps":[]}`,
		"empty replacement":        `{"kind":"supersede","step_id":"s1","steps":[]}`,
		"long title":               `{"kind":"edit","step_id":"s1","title":"` + strings.Repeat("x", 201) + `"}`,
		"invalid id":               `{"kind":"add","steps":[{"id":"bad\nidentifier","title":"New work"}]}`,
		"empty normalized title":   `{"kind":"edit","step_id":"s1","title":"   "}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := NewPlanStore(12, 4000)
			mustExecute(t, s, checkedCreateArgs())
			before, _ := s.Snapshot()
			epoch := s.CheckEpoch()
			if _, err := s.Execute(`{"verb":"revise","reason":"New finding","operations":[{"kind":"add","steps":[{"id":"new","title":"Valid addition"}]},` + op + `]}`); err == nil {
				t.Fatal("accepted malformed revision")
			}
			after, _ := s.Snapshot()
			if !reflect.DeepEqual(before, after) || epoch != s.CheckEpoch() {
				t.Fatal("rejected revision changed state or epoch")
			}
		})
	}
}

func TestRevisionTransferKeepsNewAndExistingRequirements(t *testing.T) {
	s := NewPlanStore(12, 4000)
	mustExecute(t, s, checkedCreateArgs())
	mustExecute(t, s, `{"verb":"revise","reason":"Add output verification","operations":[{"kind":"supersede","step_id":"s1","carry_checks_to":"replacement","steps":[{"id":"replacement","title":"Verify both files","checks":[{"id":"extra","description":"Inspect another file","tool":"read_file","arguments":{"path":"other"}}]}]}]}`)
	state, _ := s.Snapshot()
	if len(state.Steps) != 1 || len(state.Steps[0].Checks) != 2 || len(s.PendingChecks()) != 2 {
		t.Fatalf("transfer lost newly declared or existing criteria: %+v", state)
	}
}

func TestCreateCannotReuseEvidenceForChangedWork(t *testing.T) {
	s := NewPlanStore(12, 4000)
	mustExecute(t, s, checkedCreateArgs())
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"path":"out","line":1}`, "passed", false)
	mustExecute(t, s, `{"verb":"complete","step_id":"s1"}`)
	mustExecute(t, s, checkedCreateArgs())
	state, _ := s.Snapshot()
	if state.Steps[0].Status != StepDone || len(s.PendingChecks()) != 0 {
		t.Fatal("identical create lost fulfilled work")
	}
	mustExecute(t, s, strings.Replace(checkedCreateArgs(), `"title":"verify"`, `"title":"Verify a different approach"`, 1))
	state, _ = s.Snapshot()
	if state.Steps[0].Status == StepDone || len(s.PendingChecks()) != 1 {
		t.Fatal("changed work reused old evidence")
	}
}

func TestRevisionPersistedMetadataIsDetached(t *testing.T) {
	s := NewPlanStore(12, 4000)
	mustExecute(t, s, checkedCreateArgs())
	mustExecute(t, s, `{"verb":"revise","reason":"Inspect configuration first","operations":[{"kind":"add","steps":[{"id":"config","title":"Inspect configuration"}]}]}`)
	before, _ := s.Snapshot()
	b, _ := json.Marshal(before)
	before.Revision.Reason = "mutated"
	if len(before.Revision.Summary) > 0 {
		before.Revision.Summary[0] = "mutated"
	}
	after, _ := s.Snapshot()
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("snapshot aliases revision metadata")
	}
	mustExecute(t, s, `{"verb":"update","updates":[{"id":"config","status":"in_progress"}]}`)
	updated, _ := s.Snapshot()
	if updated.Revision == nil {
		t.Fatal("status update erased revision metadata")
	}
}

func TestRevisionNotificationDoesNotLeakIntoCheckOutcomes(t *testing.T) {
	s := NewPlanStore(12, 4000)
	var changes []PlanChange
	s.SetOnChange(func(ch PlanChange) { changes = append(changes, ch) })
	mustExecute(t, s, checkedCreateArgs())
	mustExecute(t, s, `{"verb":"revise","reason":"Add preparation","operations":[{"kind":"add","steps":[{"id":"prep","title":"Prepare"}]}]}`)
	if !changes[len(changes)-1].Revised {
		t.Fatal("missing revision notification")
	}
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"path":"out","line":1}`, "read", false)
	if changes[len(changes)-1].Revised {
		t.Fatal("evidence outcome incorrectly announced revision")
	}
}

func TestRevisionNoOpPreservesEvidenceAndVersion(t *testing.T) {
	s := NewPlanStore(12, 4000)
	mustExecute(t, s, checkedCreateArgs())
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"path":"out","line":1}`, "pass", false)
	mustExecute(t, s, `{"verb":"complete","step_id":"s1"}`)
	before, _ := s.Snapshot()
	epoch := s.CheckEpoch()
	mustExecute(t, s, `{"verb":"revise","reason":"Already arranged","operations":[{"kind":"move","step_id":"s1"},{"kind":"edit","step_id":"s1","title":"verify"}]}`)
	after, _ := s.Snapshot()
	if !reflect.DeepEqual(before, after) || s.CheckEpoch() != epoch {
		t.Fatal("no-op revision changed evidence or version")
	}
}

func TestRevisionOverflowRejectedWithoutLosingCompletedSteps(t *testing.T) {
	s := NewPlanStore(12, 900)
	mustExecute(t, s, `{"verb":"create","steps":[{"id":"s","title":"Work"}]}`)
	mustExecute(t, s, `{"verb":"complete","step_id":"s"}`)
	before, _ := s.Snapshot()
	raw := `{"verb":"revise","reason":"Expand details","operations":[{"kind":"edit","step_id":"s","note":"` + strings.Repeat("x", 1000) + `"}]}`
	if _, err := s.Execute(raw); err == nil {
		t.Fatal("accepted revision that loses completed work on rendering")
	}
	after, _ := s.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("overflow rejection mutated plan")
	}
}

func TestRevisionCanMoveToFrontAndInsertBefore(t *testing.T) {
	s := NewPlanStore(12, 4000)
	mustExecute(t, s, `{"verb":"create","steps":[{"id":"a","title":"A"},{"id":"b","title":"B"}]}`)
	mustExecute(t, s, `{"verb":"revise","reason":"Prioritize new prerequisite","operations":[{"kind":"move","step_id":"b","before_id":"a"},{"kind":"add","before_id":"b","steps":[{"id":"prep","title":"Prepare"}]}]}`)
	state, _ := s.Snapshot()
	if state.Steps[0].ID != "prep" || state.Steps[1].ID != "b" || state.Steps[2].ID != "a" {
		t.Fatalf("wrong order: %+v", state.Steps)
	}
	if _, err := s.Execute(`{"verb":"revise","reason":"Invalid anchors","operations":[{"kind":"move","step_id":"prep","before_id":"b","after_id":"a"}]}`); err == nil {
		t.Fatal("accepted ambiguous anchors")
	}
}
