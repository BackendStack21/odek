package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// P3 — check lifecycle RED tests: plan_check_replace verb, environment-denied
// checks transition to blocked (non-gating), create may always reset (old
// plan archived), and wire payloads degrade gracefully across versions.

// Blocked checks do not gate step completion — closeout honesty only.
func TestP3_BlockedCheckDoesNotGateComplete(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"denied gate","tool":"shell","arguments":{"command":"sudo true"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	epoch := s.CheckEpoch()
	// Environment denial: the tool call was refused by config/approval.
	s.RecordCheckDenied(epoch, "shell", `{"command":"sudo true"}`, "call-1")
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err != nil {
		t.Fatalf("blocked check must not gate completion, got: %v", err)
	}
	rendered, err := s.Execute(`{"verb":"get"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "blocked") {
		t.Errorf("render should surface blocked state for closeout honesty, got: %s", rendered)
	}
}

// Denied checks stay visible in PendingChecks? No — they are blocked, not
// pending; they must not appear as missing evidence in the closeout notice.
func TestP3_BlockedCheckExcludedFromPending(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"denied","tool":"shell","arguments":{"command":"x"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	s.RecordCheckDenied(s.CheckEpoch(), "shell", `{"command":"x"}`, "call-1")
	for _, p := range s.PendingChecks() {
		if p == "s1/c1" {
			t.Errorf("blocked check should not be listed pending: %v", s.PendingChecks())
		}
	}
}

// plan check_replace replaces a dead/stale check; audit-trailed via the
// revision mechanism; new check starts pending.
func TestP3_CheckReplaceVerb(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"dead","description":"never satisfiable","tool":"nonexistent_tool","arguments":{"x":1}}]}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute(`{"verb":"check_replace","step_id":"s1","check_id":"dead","justification":"env cannot run nonexistent_tool","replacement":{"id":"fresh","description":"verify via read","tool":"read_file","arguments":{"path":"out"}}}`)
	if err != nil {
		t.Fatalf("check_replace should succeed: %v", err)
	}
	if !strings.Contains(out, "fresh") {
		t.Errorf("replacement check should be present in render: %s", out)
	}
	// Old check gone, new one pending → completion still gated until evidence.
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err == nil {
		t.Error("fresh pending check must still gate completion")
	}
}

// check_replace with evidence_note marks the replacement as satisfied by
// equivalent verification that ran through other tools (spec class 5).
func TestP3_CheckReplaceWithEvidenceNote(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"dead","description":"stale","tool":"shell","arguments":{"command":"make check"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute(`{"verb":"check_replace","step_id":"s1","check_id":"dead","justification":"verified via read_file diff instead","evidence_note":"diff confirmed expected output at out:12"}`)
	if err != nil {
		t.Fatalf("evidence_note replace should succeed: %v", err)
	}
	if !strings.Contains(out, "passed") && !strings.Contains(out, "evidence") {
		t.Errorf("evidenced replacement should render as satisfied: %s", out)
	}
	if _, err := s.Execute(`{"verb":"complete","step_id":"s1"}`); err != nil {
		t.Errorf("evidenced replacement must not gate completion: %v", err)
	}
}

// create may always reset: replacing a plan that carries checked steps is
// allowed and the supersession is audit-trailed, not silently dropped.
func TestP3_CreateAlwaysResets(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"verify","tool":"shell","arguments":{"command":"true"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute(`{"verb":"create","steps":[{"id":"s2","title":"Fresh plan"}]}`)
	if err != nil {
		t.Fatalf("create over checked plan must reset (archived), got: %v", err)
	}
	if strings.Contains(out, "s1") && strings.Contains(out, "Scaffold") {
		t.Errorf("old step should not persist in the new plan: %s", out)
	}
}

// checksJSON extracts the first check object from a rendered checks payload.
func checksJSON(raw string) []byte {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return []byte("{}")
	}
	return []byte(raw[start : end+1])
}

// Wire degradation: a plan whose checks are all legacy statuses must marshal
// byte-identically to the pre-P3 wire form (no new always-on fields), and a
// blocked check must round-trip its status.
func TestP3_WireDegradation(t *testing.T) {
	s := NewPlanStore(3, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"S","checks":[{"id":"c1","description":"verify","tool":"shell","arguments":{"command":"true"}}]}]}`); err != nil {
		t.Fatal(err)
	}
	s.RecordCheckDenied(s.CheckEpoch(), "shell", `{"command":"true"}`, "call-1")
	rendered, err := s.Execute(`{"verb":"get"}`)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(rendered, " || checks:")
	if start < 0 {
		t.Fatalf("no checks marker in render: %s", rendered)
	}
	raw := rendered[start+len(" || checks:"):]
	var checks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		t.Fatalf("checks wire payload must stay JSON: %v\n%s", err, raw)
	}
	if len(checks) != 1 || checks[0].Status != "blocked" {
		t.Errorf("blocked status must round-trip on the wire, got: %+v", checks)
	}
	// The single check object must carry only legacy fields.
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal([]byte(checksJSON(raw)), &legacy); err != nil {
		t.Fatalf("check object must be a JSON object: %v", err)
	}
	for _, want := range []string{"id", "description", "tool", "arguments", "status"} {
		if _, ok := legacy[want]; !ok {
			t.Errorf("wire check object missing legacy field %q: %v", want, legacy)
		}
	}
	for k := range legacy {
		switch k {
		case "id", "description", "tool", "arguments", "status", "call_id":
		default:
			t.Errorf("wire check object gained non-legacy field %q — needs omitempty + degradation matrix", k)
		}
	}
}
