package loop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// planVerbExample returns one minimal, schema-valid argument envelope per
// verb. These are embedded verbatim into teaching errors and the tool
// description, so a change here must keep plan_usability_test.go's drift
// guard green: every example must stay executable against the current
// validation rules. Content-free — static ids only, never plan data.
func planVerbExample(verb string) string {
	switch verb {
	case "create":
		return `{"verb":"create","steps":[{"id":"s1","title":"Scaffold"}]}`
	case "update":
		return `{"verb":"update","updates":[{"id":"s1","status":"in_progress"}]}`
	case "complete":
		return `{"verb":"complete","step_id":"s1"}`
	case "revise":
		return `{"verb":"revise","operations":[{"kind":"add","steps":[{"id":"s4","title":"New step"}]}]}`
	case "get":
		return `{"verb":"get"}`
	default:
		return ""
	}
}

// teaching appends the verb's expected-fields hint and a minimal example to
// a diagnostic error, so a rejected call becomes a one-shot recovery instead
// of a blind retry.
func teaching(verb, msg string) error {
	return fmt.Errorf("%s — example: %s", msg, planVerbExample(verb))
}

// classifyPlanFailure maps a validation error to a coarse, content-free
// class for the plan_validation_failed event. Key names and static hints
// only — never step titles or plan content.
func classifyPlanFailure(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "parse args"):
		return "parse_error"
	case strings.Contains(msg, "unknown verb"):
		return "unknown_verb"
	case strings.Contains(msg, "conflicts"):
		return "alias_conflict"
	case strings.Contains(msg, "step_id cannot be combined"):
		return "alias_ambiguous"
	case strings.Contains(msg, "unknown step id"):
		return "unknown_step_id"
	case strings.Contains(msg, "requires"):
		return "missing_field"
	case strings.Contains(msg, "did you mean"):
		return "wrong_field_name"
	default:
		return "shape"
	}
}

// verbFromArgs extracts the verb for event classification without a full
// decode; an unparseable envelope reports "".
func verbFromArgs(argsJSON string) string {
	var probe struct {
		Verb string `json:"verb"`
	}
	if json.Unmarshal([]byte(argsJSON), &probe) != nil {
		return ""
	}
	return probe.Verb
}

// SetOnValidationFailure registers an optional callback fired once per
// rejected plan tool call with the verb and a coarse failure class —
// never raw arguments or plan content. Used by the engine to emit the
// plan_validation_failed runtime event.
func (s *PlanStore) SetOnValidationFailure(fn func(verb, class string)) {
	s.mu.Lock()
	s.onValidationFailure = fn
	s.mu.Unlock()
}
