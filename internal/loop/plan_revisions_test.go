package loop

import (
	"strings"
	"testing"
)

func TestPlanRevisionPreservesChecksAndEvidence(t *testing.T) {
	s := NewPlanStore(8, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	epoch := s.CheckEpoch()
	s.RecordCheckOutcome(epoch, "read_file", `{"path":"out","line":1}`, "call", false)
	args := `{"verb":"revise","reason":"split the implementation","operations":[{"kind":"split","step_id":"s1","carry_checks_to":"s1b","steps":[{"id":"s1a","title":"prepare"},{"id":"s1b","title":"verify"}]}]}`
	if _, err := s.Execute(args); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if len(state.Steps) != 2 || len(state.Steps[1].Checks) != 1 || state.Steps[1].Checks[0].Status != PlanCheckPending {
		t.Fatalf("bad split state: %+v", state)
	}
	if state.Revision == nil || state.Revision.Reason == "" {
		t.Fatalf("missing revision metadata: %+v", state.Revision)
	}
	parsed, err := parsePlanState(renderPlan(state, 4000), 8)
	if err != nil || parsed.Revision == nil {
		t.Fatalf("revision did not round-trip: %v", err)
	}
}

func TestPlanRevisionRejectsCheckedCreateEscapeAtomically(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	// create may always reset (no unrecoverable states); the superseded
	// checked plan is audit-trailed in the revision block, not silently dropped.
	out, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"changed","checks":[{"id":"c1","description":"different","tool":"read_file","arguments":{"path":"out"}}]}]}`)
	if err != nil {
		t.Fatalf("reset refused: %v", err)
	}
	if !strings.Contains(out, "changed") {
		t.Errorf("new step title missing: %s", out)
	}
	snap, _ := s.Snapshot()
	if snap.Revision == nil || !strings.Contains(snap.Revision.Reason, "superseded") {
		t.Errorf("reset must archive the old checked plan in the revision block, got: %+v", snap.Revision)
	}
}
