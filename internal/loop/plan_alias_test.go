package loop

import "testing"

func TestPlanStoreSnapshotAndRestoreAreIsolated(t *testing.T) {
	s := NewPlanStore(4, 1000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"original"}]}`); err != nil {
		t.Fatal(err)
	}
	snap, ok := s.Snapshot()
	if !ok {
		t.Fatal("missing snapshot")
	}
	snap.Steps[0].Title = "mutated snapshot"
	got, _ := s.Snapshot()
	if got.Steps[0].Title != "original" {
		t.Fatal("Snapshot exposed authoritative steps")
	}

	st := PlanState{Version: 9, Steps: []PlanStep{{ID: "r1", Title: "restored", Status: StepPending}}}
	s.Restore(st)
	st.Steps[0].Title = "mutated input"
	got, _ = s.Snapshot()
	if got.Steps[0].Title != "restored" {
		t.Fatal("Restore retained caller-owned steps")
	}
}
