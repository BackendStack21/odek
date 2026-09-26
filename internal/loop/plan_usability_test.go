package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── Teaching errors: validation failures state the expected shape ──────

func TestPlan_TeachingErrorExamplesParse(t *testing.T) {
	s := mustStore(3, 2000)
	cases := []struct {
		name string
		args string
		// the error must mention the verb's expected fields
		wantFields string
	}{
		{"update with steps", `{"verb":"update","steps":[{"id":"a","title":"A"}]}`, "updates"},
		{"complete missing step_id", `{"verb":"complete"}`, "step_id"},
		{"revise missing operations", `{"verb":"revise","reason":"why"}`, "operations"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Execute(tc.args)
			if err == nil {
				t.Fatal("expected rejection")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantFields) {
				t.Fatalf("teaching error missing expected-fields hint %q: %v", tc.wantFields, msg)
			}
			// The error must embed a parseable JSON example for the verb.
			start := strings.Index(msg, "example: ")
			if start < 0 {
				t.Fatalf("teaching error carries no example: %v", msg)
			}
			raw := msg[start+len("example: "):]
			end := strings.LastIndex(raw, "}")
			if end < 0 {
				t.Fatalf("unterminated example in: %v", msg)
			}
			var probe map[string]any
			if jerr := json.Unmarshal([]byte(raw[:end+1]), &probe); jerr != nil {
				t.Fatalf("example is not valid JSON (%v): %v", jerr, msg)
			}
			if probe["verb"] == "" {
				t.Fatalf("example lacks verb: %v", msg)
			}
		})
	}
}

// Teaching errors are content-free: they never echo step titles or plan body.
func TestPlan_TeachingErrorLeakFree(t *testing.T) {
	s := mustStore(3, 2000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"secret1","title":"TopSecretTitle"}]}`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Execute(`{"verb":"update","steps":[{"id":"secret1","title":"TopSecretTitle"}]}`)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if strings.Contains(err.Error(), "TopSecretTitle") {
		t.Fatalf("teaching error leaked step content: %v", err)
	}
}

// ── Field-name aliases (shape-level leniency stays forbidden) ──────────

func TestPlan_AliasCompleteID(t *testing.T) {
	s := mustStore(3, 2000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"A"},{"id":"s2","title":"B"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(`{"verb":"complete","id":"s1"}`); err != nil {
		t.Fatalf("complete should accept id alias: %v", err)
	}
	st, _ := s.Snapshot()
	if st.Steps[0].Status != StepDone {
		t.Fatalf("alias complete did not mark s1 done: %+v", st.Steps[0])
	}
}

func TestPlan_AliasUpdateStepID(t *testing.T) {
	s := mustStore(3, 2000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"A"},{"id":"s2","title":"B"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(`{"verb":"update","step_id":"s2","status":"in_progress"}`); err != nil {
		t.Fatalf("update should accept single step_id form: %v", err)
	}
	st, _ := s.Snapshot()
	if st.Steps[1].Status != StepInProgress {
		t.Fatalf("alias update did not apply: %+v", st.Steps[1])
	}
}

func TestPlan_AliasPrecedenceHardErrors(t *testing.T) {
	s := mustStore(3, 2000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"A"},{"id":"s2","title":"B"}]}`); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot()

	// step_id alongside a multi-element updates array: hard error.
	_, err := s.Execute(`{"verb":"update","step_id":"s1","updates":[{"id":"s1","status":"done"},{"id":"s2","status":"done"}]}`)
	if err == nil || !strings.Contains(err.Error(), "step_id") {
		t.Fatalf("step_id + multi updates must be a hard error, got: %v", err)
	}
	// step_id disagreeing with updates[0].id: hard error, never silent pick.
	_, err = s.Execute(`{"verb":"update","step_id":"s1","updates":[{"id":"s2","status":"in_progress"}]}`)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting step_id/updates[0].id must be a hard error, got: %v", err)
	}
	// agreeing forms are accepted (updates[0].id wins, no behavior change)
	if _, err := s.Execute(`{"verb":"update","step_id":"s1","updates":[{"id":"s1","status":"in_progress"}]}`); err != nil {
		t.Fatalf("agreeing step_id + updates[0].id should apply: %v", err)
	}
	after, _ := s.Snapshot()
	if after.Version < before.Version {
		t.Fatal("state regressed")
	}
}

// ── Validation telemetry: SetOnValidationFailure ───────────────────────

func TestPlan_ValidationFailureEvent(t *testing.T) {
	s := mustStore(3, 2000)
	type failure struct{ verb, class string }
	var got []failure
	s.SetOnValidationFailure(func(verb, class string) {
		got = append(got, failure{verb, class})
	})
	if _, err := s.Execute(`{"verb":"update","steps":[]}`); err == nil {
		t.Fatal("expected rejection")
	}
	if _, err := s.Execute(`{"verb":"bogus"}`); err == nil {
		t.Fatal("expected rejection")
	}
	if _, err := s.Execute(`{"verb":"complete","step_id":"ghost"}`); err == nil {
		t.Fatal("expected rejection")
	}
	// Successful calls must NOT fire the failure callback.
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"s1","title":"A"}]}`); err != nil {
		t.Fatal(err)
	}
	want := []failure{{"update", "missing_field"}, {"unknown", "unknown_verb"}, {"complete", "unknown_step_id"}}
	if len(got) != len(want) {
		t.Fatalf("failures = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("failure[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Aliases on a fresh store (no plan yet) must fail with a teaching error,
// not panic or silently succeed.
func TestPlan_AliasBeforeCreate(t *testing.T) {
	s := mustStore(3, 2000)
	if _, err := s.Execute(`{"verb":"complete","id":"s1"}`); err == nil ||
		!strings.Contains(err.Error(), "unknown step id") {
		t.Fatalf("complete alias before create = %v, want unknown step id", err)
	}
	if _, err := s.Execute(`{"verb":"update","step_id":"s1","status":"done"}`); err == nil ||
		!strings.Contains(err.Error(), "unknown step id") {
		t.Fatalf("update alias before create = %v, want unknown step id", err)
	}
}

// ── Canonical examples in the tool description stay schema-valid ───────

func TestPlan_DescriptionExamplesDriftGuard(t *testing.T) {
	desc := (&PlanTool{Store: mustStore(3, 2000)}).Description()
	// Examples are embedded as bare JSON objects in the description; a
	// json.Decoder walk extracts each one without delimiter ambiguity.
	// Examples must execute against the validation rules, in the order the
	// description emits them: create seeds the plan the update example
	// mutates, so ONE fresh store runs the whole sequence per test
	// invocation (-count>1 safe).
	rest := desc
	var examples []string
	for {
		idx := strings.Index(rest, "{\"verb\":")
		if idx < 0 {
			break
		}
		rest = rest[idx:]
		var full map[string]any
		d2 := json.NewDecoder(strings.NewReader(rest))
		if err := d2.Decode(&full); err != nil {
			t.Fatalf("description example is not valid JSON (%v): %.80s", err, rest)
		}
		raw := rest[:d2.InputOffset()]
		rest = rest[d2.InputOffset():]
		if full["verb"] == "" {
			t.Fatalf("description example lacks verb: %s", raw)
		}
		examples = append(examples, raw)
	}
	if len(examples) < 2 {
		t.Fatalf("expected at least 2 examples in description, got %d", len(examples))
	}
	shared := mustStore(5, 4000)
	for _, raw := range examples {
		if _, err := shared.Execute(raw); err != nil {
			t.Fatalf("description example fails validation (%v): %s", err, raw)
		}
	}
}

// ── Schema presents per-verb field mapping ─────────────────────────────

func TestPlan_SchemaVerbFieldMap(t *testing.T) {
	schema := (&PlanTool{Store: mustStore(3, 2000)}).Schema()
	m, ok := schema.(map[string]any)
	if !ok {
		t.Fatal("schema is not a map")
	}
	props, _ := m["properties"].(map[string]any)
	verb, ok := props["verb"].(map[string]any)
	if !ok {
		t.Fatal("schema lacks verb property")
	}
	vd, _ := verb["description"].(string)
	for _, want := range []string{"create → steps", "update → updates", "complete → step_id", "revise → operations", "get →"} {
		if !strings.Contains(vd, want) {
			t.Fatalf("verb description missing mapping %q; got: %s", want, vd)
		}
	}
}
