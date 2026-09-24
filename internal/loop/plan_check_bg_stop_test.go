package loop

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/tool"
)

// bgStopCall returns a representative non-matching bg_stop tool call.
func bgStopCall() session.ToolCall {
	var tc session.ToolCall
	tc.Function.Name = "bg_stop"
	tc.Function.Arguments = `{"job_id":"bg_00000009"}`
	return tc
}

// bg_stop terminates an in-memory job registry entry; it writes nothing to
// the filesystem. It must not be classified as having unknown effects, or
// every batched plan-check acceptance is wiped by the transcript-order
// invalidation policy.
func TestEffectsFor_BgStopIsEffectFree(t *testing.T) {
	fx := effectsFor(bgStopCall())
	if fx.unknown || len(fx.writes) > 0 {
		t.Fatalf("bg_stop classified as mutating: %+v", fx)
	}
}

// Regression: a passing check followed by a non-matching bg_stop in the same
// batch must keep its recorded evidence. Today bg_stop lands in the unknown
// branch of executionEffects and InvalidateChecks erases every check.
func TestRecordCheckResult_BgStopPreservesEvidence(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	e := &Engine{planStore: s, registry: tool.NewRegistry(nil)}
	epoch := s.CheckEpoch()

	var readCall session.ToolCall
	readCall.Function.Name = "read_file"
	readCall.Function.Arguments = `{"path":"out","line":1}`
	e.recordPlanCheckResult(epoch, readCall, "call-1", false)
	if got := s.PendingChecks(); len(got) != 0 {
		t.Fatalf("matching check did not record: %v", got)
	}

	e.recordPlanCheckResult(epoch, bgStopCall(), "call-2", false)
	if got := s.PendingChecks(); len(got) != 0 {
		t.Fatalf("non-matching bg_stop wiped check evidence: %v", got)
	}
	state, ok := s.Snapshot()
	if !ok || state.Steps[0].Checks[0].Status != PlanCheckPassed {
		t.Fatalf("check status after bg_stop: %+v", state.Steps[0])
	}
}

// Superseding a step whose checks are stale or unpassable must be possible
// without carrying them forward: the replacement declares its own fresh
// checks and the old ones are dropped with the replaced step.
func TestRevise_SupersedeWithoutCarryDropsStaleChecks(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	raw := `{"verb":"revise","reason":"replace unpassable placeholder check","operations":[{"kind":"supersede","step_id":"s1","steps":[{"id":"s1","title":"verify again","checks":[{"id":"c2","description":"fresh evidence","tool":"read_file","arguments":{"path":"out2"}}]}]}]}`
	if _, err := s.Execute(raw); err != nil {
		t.Fatalf("supersede without carry_checks_to rejected: %v", err)
	}
	state, ok := s.Snapshot()
	if !ok || len(state.Steps) != 1 {
		t.Fatalf("unexpected steps after supersede: %+v", state)
	}
	checks := state.Steps[0].Checks
	if len(checks) != 1 || checks[0].ID != "c2" {
		t.Fatalf("stale checks survived supersede: %+v", checks)
	}
	if checks[0].Status != PlanCheckPending || !strings.Contains(s.PendingChecks()[0], "s1/c2") {
		t.Fatalf("fresh check not pending: %+v / %v", checks[0], s.PendingChecks())
	}
}

// A checked requirement can never be silently dropped: superseding it with
// a checkless replacement and no carry_checks_to must be rejected, and the
// rejection must leave the original evidence intact.
func TestRevise_SupersedeChecklessReplacementRejected(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	raw := `{"verb":"revise","reason":"attempt to drop the check","operations":[{"kind":"supersede","step_id":"s1","steps":[{"id":"s1","title":"Replacement"}]}]}`
	if _, err := s.Execute(raw); err == nil {
		t.Fatal("checkless supersede without carry_checks_to was accepted")
	}
	if got := s.PendingChecks(); len(got) != 1 {
		t.Fatalf("check evidence did not survive rejection: %v", got)
	}
}

// The split variant shares the supersede code path but is a distinct
// operation: a checked step split without carry_checks_to is legal exactly
// when at least one replacement declares fresh checks, which then replace
// the originals.
func TestRevise_SplitWithFreshChecksReplacesOriginals(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	raw := `{"verb":"revise","reason":"split with fresh evidence","operations":[{"kind":"split","step_id":"s1","steps":[{"id":"s1a","title":"A","checks":[{"id":"c2","description":"fresh evidence","tool":"read_file","arguments":{"path":"out2"}}]},{"id":"s1b","title":"B"}]}]}`
	if _, err := s.Execute(raw); err != nil {
		t.Fatalf("split with fresh checks rejected: %v", err)
	}
	state, ok := s.Snapshot()
	if !ok || len(state.Steps) != 2 {
		t.Fatalf("unexpected steps after split: %+v", state)
	}
	checks := state.Steps[0].Checks
	if len(checks) != 1 || checks[0].ID != "c2" || checks[0].Status != PlanCheckPending {
		t.Fatalf("fresh checks did not replace originals: %+v", checks)
	}
	if got := s.PendingChecks(); len(got) != 1 || !strings.Contains(got[0], "s1a/c2") {
		t.Fatalf("pending checks after split: %v", got)
	}
}
