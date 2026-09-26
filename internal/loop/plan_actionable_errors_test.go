package loop

import (
	"strings"
	"testing"
)

// P2 — actionable-diagnostics RED tests. Every validation failure must carry:
// (1) the failing field's path, (2) the rule that failed, (3) a retryable
// flag, and gating errors must name the blocking check ids plus the exact
// satisfying call. Run with -run TestP2.

func mustExecP2(t *testing.T, s *PlanStore, args string) {
	t.Helper()
	if _, err := s.Execute(args); err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
}

// Field path + rule for check description violations (spec class 2: opaque
// nested validation hides whether the description is empty-after-normalize
// or over the character cap).
func TestP2_CheckDescriptionErrorNamesRule(t *testing.T) {
	s := NewPlanStore(3, 2000)
	long := strings.Repeat("x", 201)
	_, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"` + long + `","tool":"shell"}]}]}`)
	if err == nil {
		t.Fatal("want error for over-length description")
	}
	msg := err.Error()
	for _, want := range []string{"check[0].description", "max 200", "retryable: true"} {
		if !strings.Contains(msg, want) {
			t.Errorf("long-description error missing %q, got: %s", want, msg)
		}
	}

	s2 := NewPlanStore(3, 2000)
	_, err = s2.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"   ","tool":"shell"}]}]}`)
	if err == nil {
		t.Fatal("want error for empty-after-normalize description")
	}
	if msg := err.Error(); !strings.Contains(msg, "check[0].description") || !strings.Contains(msg, "empty after trimming") {
		t.Errorf("empty-description error should name field path and rule, got: %s", msg)
	}
}

// Field path for step-level validation errors.
func TestP2_StepErrorNamesFieldPath(t *testing.T) {
	s := NewPlanStore(3, 2000)
	_, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"   "}]}`)
	if err == nil {
		t.Fatal("want error for whitespace-only title")
	}
	if msg := err.Error(); !strings.Contains(msg, "steps[0].title") {
		t.Errorf("title error should name steps[0].title, got: %s", msg)
	}
}

// Gating errors must name the blocking check ids and the exact satisfying
// call (spec classes 5+7: evidence/check drift and completion gate).
func TestP2_GatingErrorNamesCheckIDsAndSatisfyingCall(t *testing.T) {
	s := NewPlanStore(3, 2000)
	mustExecP2(t, s, `{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"gate1","description":"run build","tool":"shell","arguments":{"command":"go build ./..."}},{"id":"gate2","description":"run tests","tool":"shell","arguments":{"command":"go test ./..."}}]}]}`)
	_, err := s.Execute(`{"verb":"complete","step_id":"s1"}`)
	if err == nil {
		t.Fatal("want gating error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gate1") || !strings.Contains(msg, "gate2") {
		t.Errorf("gating error should name blocking check ids, got: %s", msg)
	}
	if !strings.Contains(msg, `"command":"go build ./..."`) {
		t.Errorf("gating error should carry the exact satisfying call for gate1, got: %s", msg)
	}
}

// Unrecoverable-state errors (create-refuses-reset) must state the escape
// create over a checked plan now resets (P3): the old plan is archived in
// the revision block, not refused — no unrecoverable states.
func TestP2_DeadlockErrorNamesEscapeHatch(t *testing.T) {
	s := NewPlanStore(3, 4000)
	mustExecP2(t, s, `{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"verify","tool":"shell","arguments":{"command":"true"}}]}]}`)
	out, err := s.Execute(`{"verb":"create","steps":[{"id":"s2","title":"Other"}]}`)
	if err != nil {
		t.Fatalf("create must always reset — no deadlock: %v", err)
	}
	if !strings.Contains(out, "s2") {
		t.Errorf("new plan should contain s2: %s", out)
	}
}
