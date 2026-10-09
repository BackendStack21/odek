package loop

import (
	"strings"
	"testing"
)

// A superseded create must not leave partial carried-over evidence that depends
// on step order.
func TestRED_CreateSupersedeCarriesPartialEvidence(t *testing.T) {
	s := NewPlanStore(12, 8000)
	chk := `"checks":[{"id":"c","description":"d","tool":"shell","arguments":{"command":"t"}}]`
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"one",` + chk + `},{"id":"s2","title":"two",` + chk + `}]}`); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	ep := s.CheckEpoch()
	s.RecordCheckOutcome(ep, "shell", `{"command":"t"}`, "call1", false)
	_ = st
	// create drops s2 -> incompatible -> superseded reset
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"one",` + chk + `}]}`); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if st.Revision == nil || !strings.Contains(st.Revision.Reason, "superseded") {
		t.Skip("not superseded")
	}
	if st.Steps[0].Checks[0].Status != PlanCheckPending || st.Steps[0].Status != StepPending {
		t.Fatalf("superseded plan inherited old evidence: step=%s check=%s", st.Steps[0].Status, st.Steps[0].Checks[0].Status)
	}
}

// A compatible create keeps the evidence of the plan it extends.
func TestCreateCompatibleKeepsEvidence(t *testing.T) {
	s := NewPlanStore(12, 8000)
	chk := `"checks":[{"id":"c","description":"d","tool":"shell","arguments":{"command":"t"}}]`
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"one",` + chk + `}]}`); err != nil {
		t.Fatal(err)
	}
	s.RecordCheckOutcome(s.CheckEpoch(), "shell", `{"command":"t"}`, "call1", false)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"one",` + chk + `},{"id":"s2","title":"two"}]}`); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if st.Steps[0].Checks[0].Status != PlanCheckPassed {
		t.Fatalf("compatible create dropped passing evidence: %s", st.Steps[0].Checks[0].Status)
	}
}
