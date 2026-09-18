package loop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func checkedCreateArgs() string {
	return `{"verb":"create","steps":[{"id":"s1","title":"verify","checks":[{"id":"c1","description":"Read the output","tool":"read_file","arguments":{"path":"out","line":1}}]}]}`
}

func TestPlanChecks_RenderParseAndRequireOutcomes(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	state, ok := s.Snapshot()
	if !ok || len(state.Steps[0].Checks) != 1 {
		t.Fatalf("missing check: %+v", state)
	}
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err == nil {
		t.Fatal("completed a step before its check passed")
	}
	rendered := renderPlan(state, 4000)
	parsed, err := parsePlanState(rendered, 4)
	if err != nil {
		t.Fatalf("rendered checked plan did not parse: %v\n%s", err, rendered)
	}
	if len(parsed.Steps[0].Checks) != 1 || parsed.Steps[0].Checks[0].Status != PlanCheckPending {
		t.Fatalf("parsed check state: %+v", parsed.Steps[0].Checks)
	}
	epoch := s.CheckEpoch()
	if !s.MatchesCheck("read_file", `{"line":1,"path":"out"}`) {
		t.Fatal("argument key order did not match")
	}
	s.RecordCheckOutcome(epoch, "read_file", `{"path":"out","line":1}`, "call-1", false)
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err != nil {
		t.Fatal(err)
	}
	if got := s.PendingChecks(); len(got) != 0 {
		t.Fatalf("pending checks after pass: %v", got)
	}
}

func TestPlanChecks_RuntimeInvalidationAndEpoch(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	epoch := s.CheckEpoch()
	s.RecordCheckOutcome(epoch, "read_file", `{"path":"out","line":1}`, "call-1", false)
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err != nil {
		t.Fatal(err)
	}
	s.InvalidateChecks()
	state, _ := s.Snapshot()
	if state.Steps[0].Status != StepInProgress || state.Steps[0].Checks[0].Status != PlanCheckPending {
		t.Fatalf("invalidation did not reopen check: %+v", state.Steps[0])
	}
	s.mu.Lock()
	s.plan.Steps[0].Status = StepDone
	s.mu.Unlock()
	s.RecordCheckOutcome(epoch, "read_file", `{"path":"out","line":1}`, "failed", true)
	state, _ = s.Snapshot()
	if state.Steps[0].Status != StepInProgress || len(s.PendingChecks()) != 1 || !strings.Contains(s.PendingChecks()[0], "s1/c1") {
		t.Fatalf("failed outcome did not reopen step: %+v", state.Steps[0])
	}
}

func TestPlanChecks_RejectsForgedOrOversizedDeclarations(t *testing.T) {
	s := NewPlanStore(4, 4000)
	for _, args := range []string{
		`{"verb":"create","steps":[{"id":"s1","title":"x","checks":[{"id":"c","description":"x","tool":"plan","arguments":{}}]}]}`,
		`{"verb":"create","steps":[{"id":"s1","title":"x","checks":[{"id":"c","description":"x","tool":"read_file","arguments":[] }]}]}`,
		`{"verb":"create","steps":[{"id":"s1","title":"x","checks":[{"id":"c","description":"x","tool":"read_file","arguments":{"x":"` + strings.Repeat("a", 4100) + `"}}]}]}`,
	} {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted invalid check declaration: %s", args[:minPlanTest(80, len(args))])
		}
	}
}

func minPlanTest(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestPlanChecks_CompletedUpdateCannotLoseEvidence(t *testing.T) {
	s := NewPlanStore(4, 700)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"path":"out","line":1}`, strings.Repeat("x", 128), false)
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot()
	update := `{"verb":"update","updates":[{"id":"s1","note":"` + strings.Repeat("x", 600) + `"}]}`
	if _, err := s.Execute(update); err == nil {
		t.Fatal("accepted overflowing completed check")
	}
	after, _ := s.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected update changed state")
	}
	rendered := renderPlan(after, 700)
	parsed, err := parsePlanState(rendered, 4)
	if err != nil || len(parsed.Steps) != 1 || len(parsed.Steps[0].Checks) != 1 {
		t.Fatalf("lost completed checks: %v %s", err, rendered)
	}
}

func TestPlanChecks_ReservedDelimiterAndLegacyCompatibility(t *testing.T) {
	s := NewPlanStore(4, 4000)
	legacy := `{"verb":"create","steps":[{"id":"legacy","title":"literal || checks: text"}]}`
	if _, err := s.Execute(legacy); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	parsed, err := parsePlanState(renderPlan(state, 4000), 4)
	if err != nil || parsed.Steps[0].Title != state.Steps[0].Title {
		t.Fatalf("legacy delimiter: %v", err)
	}
	mixed := strings.Replace(checkedCreateArgs(), `"title":"verify"`, `"title":"literal || checks: text"`, 1)
	if _, err := s.Execute(mixed); err == nil {
		t.Fatal("accepted ambiguous checked title")
	}
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(`{"verb":"update","updates":[{"id":"s1","note":"literal || checks: text"}]}`); err == nil {
		t.Fatal("accepted ambiguous checked note")
	}
}

func TestPlanChecks_NormalizedEvidenceAndStaleEpoch(t *testing.T) {
	s := NewPlanStore(4, 4000)
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	oldEpoch := s.CheckEpoch()
	if _, err := s.Execute(checkedCreateArgs()); err != nil {
		t.Fatal(err)
	}
	s.RecordCheckOutcome(oldEpoch, "read_file", `{"line":1,"path":"out"}`, "old", false)
	if len(s.PendingChecks()) != 1 {
		t.Fatal("stale epoch supplied evidence")
	}
	id := strings.Repeat("\n😀", 200)
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"line":1,"path":"out"}`, id, false)
	first, _ := s.Snapshot()
	s.RecordCheckOutcome(s.CheckEpoch(), "read_file", `{"line":1,"path":"out"}`, id, false)
	second, _ := s.Snapshot()
	if first.Version != second.Version {
		t.Fatal("normalized identical call ID bumped version")
	}
	if _, err := parsePlanState(renderPlan(second, 4000), 4); err != nil {
		t.Fatal(err)
	}
	s.Restore(second)
	restored, _ := s.Snapshot()
	if len(s.PendingChecks()) != 1 || restored.Steps[0].Checks[0].CallID != "" {
		t.Fatal("restored evidence trusted")
	}
}

func TestPlanChecks_ArgumentsAreIsolatedAndStrict(t *testing.T) {
	s := NewPlanStore(4, 4000)
	args := strings.Replace(checkedCreateArgs(), `"line":1`, `"line":1,"nested":{"values":[1,2]}`, 1)
	if _, err := s.Execute(args); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot()
	snap.Steps[0].Checks[0].Arguments["nested"].(map[string]any)["values"].([]any)[0] = json.Number("99")
	if !s.MatchesCheck("read_file", `{"path":"out","line":1,"nested":{"values":[1,2]}}`) {
		t.Fatal("snapshot aliases stored arguments")
	}
	for _, raw := range []string{`{} {}`, `{} junk`, `[]`, `null`} {
		if _, err := canonicalPlanArguments([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	forged := strings.Replace(args, `"description":"Read the output"`, `"description":"Read the output","status":"passed","call_id":"forged"`, 1)
	if _, err := s.Execute(forged); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingChecks()) != 1 {
		t.Fatal("model self-certified check")
	}
}
