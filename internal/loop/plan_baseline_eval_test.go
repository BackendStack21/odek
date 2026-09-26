package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── P0 baseline eval harness ──────────────────────────────────────────
// Measures how the plan tool's fail-closed validation responds to scripted
// model-style call sequences — including realistic malformed/misdriven
// envelopes. This is a measurement, not an assertion: the harness always
// passes (barring sanity checks on itself) so the printed taxonomy is a
// baseline yardstick that later packages can re-run and diff.

// Failure taxonomy for the first failing call of each scenario.
const (
	ClassNone                = "none"
	ClassVerbPayloadMismatch = "verb_payload_mismatch"
	ClassUnknownField        = "unknown_field"
	ClassOpaqueValidation    = "opaque_validation"
	ClassDeadCheckBlock      = "dead_check_block"
	ClassDeadlock            = "deadlock"
)

type planScenario struct {
	name string
	// calls are the JSON argument envelopes, exactly as an LLM would send.
	calls []string
	// classify maps the first error text to a taxonomy class.
	classify func(errText string) string
}

func classifyDefault(errText string) string {
	// A verb receiving another verb's payload array is a verb/payload
	// mismatch, not a generic unknown-field error.
	mismatchKeys := []string{
		"requires 'steps' (array of {id,title,note,checks}); received keys: [operations",
		"requires 'steps' (array of {id,title,note,checks}); received keys: [updates",
		"requires 'updates' (array of {id,status,note}); received keys: [steps",
		"requires 'operations'",
	}
	for _, marker := range mismatchKeys {
		if strings.Contains(errText, marker) {
			return ClassVerbPayloadMismatch
		}
	}
	switch {
	case strings.Contains(errText, "conflicts"),
		strings.Contains(errText, "step_id cannot be combined"):
		return ClassVerbPayloadMismatch
	case strings.Contains(errText, "did you mean"),
		strings.Contains(errText, "unknown verb"):
		return ClassUnknownField
	case strings.Contains(errText, "has checks that have not passed"):
		return ClassDeadCheckBlock
	case strings.Contains(errText, "use revise"):
		return ClassDeadlock
	default:
		return ClassOpaqueValidation
	}
}

func baselineScenarios() []planScenario {
	longDesc := strings.Repeat("x", 201)
	return []planScenario{
		// ── Misdriven scenarios (should fail) ──
		{
			name: "verb/payload mismatch: create with operations array",
			calls: []string{
				`{"verb":"create","operations":[{"kind":"add","steps":[{"id":"s1","title":"Scaffold"}]}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "verb/payload mismatch: create using updates array",
			calls: []string{
				`{"verb":"create","updates":[{"id":"s1","status":"done"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "verb/payload mismatch: update using steps array",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"}]}`,
				`{"verb":"update","steps":[{"id":"s2","title":"Wire"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "wrong field names: stepID and status on create",
			calls: []string{
				`{"verb":"create","stepID":"s1","title":"Scaffold","status":"done"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "unknown verb: add_step",
			calls: []string{
				`{"verb":"add_step","steps":[{"id":"s1","title":"Scaffold"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "single-step alias misuse: complete with id on create",
			calls: []string{
				`{"verb":"complete","id":"s1","steps":[{"id":"s1","title":"Scaffold"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "revise with missing operations",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"}]}`,
				`{"verb":"revise","reason":"drop scaffold"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "check description too long",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold","checks":[{"id":"c1","description":"` + longDesc + `","tool":"shell"}]}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "check description empty",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold","checks":[{"id":"c1","description":"","tool":"shell"}]}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "dead check blocks complete",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold","checks":[{"id":"gate","description":"external gate never satisfied","tool":"nonexistent_tool","arguments":{"x":1}}]}]}`,
				`{"verb":"complete","step_id":"s1"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "create-refuses-reset deadlock over checked plan",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold","checks":[{"id":"c1","description":"verify","tool":"shell","arguments":{"command":"true"}}]}]}`,
				`{"verb":"create","steps":[{"id":"s2","title":"Different"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "complete unknown step id",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"}]}`,
				`{"verb":"complete","step_id":"s9"}`,
			},
			classify: classifyDefault,
		},

		// ── Well-formed scenarios (should fully succeed) ──
		{
			name: "happy path: create, update, complete, get",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire"}]}`,
				`{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`,
				`{"verb":"complete","step_id":"s1"}`,
				`{"verb":"get"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: single step create and complete",
			calls: []string{
				`{"verb":"create","steps":[{"id":"only","title":"Do it"}]}`,
				`{"verb":"complete","step_id":"only"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: update with note and blocked status",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire"}]}`,
				`{"verb":"update","updates":[{"id":"s2","status":"blocked","note":"waiting on creds"}]}`,
				`{"verb":"update","updates":[{"id":"s2","status":"in_progress"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: revise move",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire"},{"id":"s3","title":"Test"}]}`,
				`{"verb":"revise","reason":"order fix","operations":[{"kind":"move","step_id":"s2","before_id":"s1"}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: revise add step",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"}]}`,
				`{"verb":"revise","reason":"needs test phase","operations":[{"kind":"add","steps":[{"id":"s2","title":"Test"}]}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: checks defined and completed",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold","checks":[{"id":"c1","description":"run build","tool":"shell","arguments":{"command":"go build ./..."}}]}]}`,
				`{"verb":"complete","step_id":"s1"}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: revise split",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire"}]}`,
				`{"verb":"revise","reason":"split wire phase","operations":[{"kind":"split","step_id":"s2","steps":[{"id":"s2","title":"Wire config"},{"id":"s3","title":"Wire auth"}]}]}`,
			},
			classify: classifyDefault,
		},
		{
			name: "happy path: complete with id alias on update",
			calls: []string{
				`{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire"}]}`,
				`{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`,
				`{"verb":"complete","step_id":"s1"}`,
				`{"verb":"update","updates":[{"id":"s2","status":"done"}]}`,
			},
			classify: classifyDefault,
		},
	}
}

// TestPlanBaselineEval runs every scenario against a fresh PlanStore and
// prints a taxonomy summary. The first call's outcome is the "first-attempt"
// signal; the recorded class comes from the first error anywhere in the
// sequence, because dead-check and deadlock failures strike mid-sequence.
func TestPlanBaselineEval(t *testing.T) {
	scenarios := baselineScenarios()
	if len(scenarios) != 20 {
		t.Fatalf("scenario count = %d, want 20", len(scenarios))
	}

	type result struct {
		name    string
		firstOK bool
		firstErr string
		class   string
	}
	results := make([]result, 0, len(scenarios))
	classCounts := map[string]int{}

	for _, sc := range scenarios {
		if len(sc.calls) == 0 {
			t.Fatalf("scenario %q has no calls", sc.name)
		}
		s := NewPlanStore(3, 2000)
		var r result
		r.name = sc.name
		for i, call := range sc.calls {
			_, err := s.Execute(call)
			if i == 0 {
				r.firstOK = err == nil
			}
			if err != nil {
				r.firstErr = err.Error()
				r.class = sc.classify(err.Error())
				break
			}
		}
		if r.class == "" {
			r.class = ClassNone
		}
		classCounts[r.class]++
		results = append(results, r)
	}

	// Sanity: every scenario produced exactly one classification.
	total := 0
	for _, c := range classCounts {
		total += c
	}
	if total != len(results) || len(results) != len(scenarios) {
		t.Fatalf("class totals = %d, results = %d, scenarios = %d", total, len(results), len(scenarios))
	}

	// Summary table (measurement output).
	order := []string{ClassNone, ClassVerbPayloadMismatch, ClassUnknownField, ClassOpaqueValidation, ClassDeadCheckBlock, ClassDeadlock}
	t.Logf("═══ P0 plan-tool baseline eval — %d scenarios ═══", len(scenarios))
	t.Logf("%-52s %5s %-22s %s", "SCENARIO", "1ST", "CLASS", "FIRST ERROR")
	for _, r := range results {
		first := "ok"
		if !r.firstOK {
			first = "FAIL"
		}
		errTxt := r.firstErr
		if len(errTxt) > 80 {
			errTxt = errTxt[:77] + "..."
		}
		t.Logf("%-52s %5s %-22s %s", r.name, first, r.class, errTxt)
	}
	t.Logf("─── per-class counts ───")
	for _, c := range order {
		t.Logf("  %-24s %d", c, classCounts[c])
	}
	firstAttemptOK := classCounts[ClassNone]
	t.Logf("first-attempt success rate: %d/%d (%.0f%%)", firstAttemptOK, len(scenarios), 100*float64(firstAttemptOK)/float64(len(scenarios)))

	// Ensure the class consts marshal sanely for downstream diff tooling.
	for _, c := range order {
		if _, err := json.Marshal(c); err != nil {
			t.Fatalf("class %q: %v", c, err)
		}
	}
}
