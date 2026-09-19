package loop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Argument-resilience layer for the plan tool (see
// .plans/PLAN_TOOL_ARG_RESILIENCE_PLAN.md). Fail-closed validation is kept —
// every rejection is typed and leaves state untouched — but rejections carry
// diagnostics (the keys actually received, the shape expected) so a model can
// correct the call in one retry instead of guessing.

// planStepList unmarshals a steps array tolerantly: object entries decode
// normally; bare string entries coerce to a title with no id (an id is
// auto-generated downstream). Ids exist only to target updates, so a missing
// id is recoverable, not ambiguous.
type planStepList []planStepArg

func (l *planStepList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make([]planStepArg, 0, len(raw))
	for i, item := range raw {
		trimmed := bytes.TrimSpace(item)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var title string
			if err := json.Unmarshal(trimmed, &title); err != nil {
				return fmt.Errorf("steps[%d]: %w", i, err)
			}
			out = append(out, planStepArg{Title: title})
			continue
		}
		var step planStepArg
		if err := json.Unmarshal(trimmed, &step); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
		out = append(out, step)
	}
	*l = out
	return nil
}

// fillAutoStepIDs assigns ids to entries that arrived without one. Ids only
// target updates, so they can be machine-chosen: s1, s2, … skipping ids
// already used (explicit, previously assigned, or present in reserved — ids
// of steps already in the plan for revise paths), keeping them addressable
// by later update/complete calls without collisions.
func fillAutoStepIDs(in []planStepArg, reserved map[string]bool) []planStepArg {
	seen := make(map[string]bool, len(in)+len(reserved))
	for k := range reserved {
		seen[k] = true
	}
	for _, s := range in {
		// create() trims ids before validating; seed the dedupe map with the
		// trimmed form so " s1" and an auto-assigned s1 cannot collide.
		if id := strings.TrimSpace(s.ID); id != "" {
			seen[id] = true
		}
	}
	next := func() string {
		for n := 1; ; n++ {
			cand := fmt.Sprintf("s%d", n)
			if !seen[cand] {
				seen[cand] = true
				return cand
			}
		}
	}
	for i := range in {
		if in[i].ID == "" {
			in[i].ID = next()
		}
	}
	return in
}

// planRawEnvelope parses the call into raw key/value pairs for diagnostics.
// Returns nil map when the payload is not a JSON object.
func planRawEnvelope(argsJSON string) map[string]json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil
	}
	return raw
}

// planReceivedKeys lists the non-verb top-level keys of the call, sorted.
func planReceivedKeys(raw map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		if k == "verb" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// keyList renders a key slice for error messages. Keys are model-controlled
// JSON strings — each is clamped and the whole list is bounded so hostile
// oversized keys cannot flood the context through a diagnostic echo.
const (
	maxDiagKeyChars = 48
	maxDiagKeys     = 8
)

func keyList(keys []string) string {
	if len(keys) > maxDiagKeys {
		keys = append(append([]string{}, keys[:maxDiagKeys]...), fmt.Sprintf("+%d more", len(keys)-maxDiagKeys))
	}
	for i, k := range keys {
		if len(k) > maxDiagKeyChars {
			keys[i] = k[:maxDiagKeyChars] + "…"
		}
	}
	return "[" + strings.Join(keys, ", ") + "]"
}

// stepShapedArray reports whether the raw value is a non-empty array whose
// first element is an object carrying step fields (id or title) — i.e. the
// model supplied a step list under the wrong key.
func stepShapedArray(v json.RawMessage) bool {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var arr []json.RawMessage
	if json.Unmarshal(trimmed, &arr) != nil || len(arr) == 0 {
		return false
	}
	first := bytes.TrimSpace(arr[0])
	if len(first) == 0 || first[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(first, &obj) != nil {
		return false
	}
	_, hasID := obj["id"]
	_, hasTitle := obj["title"]
	return hasID || hasTitle
}

// nestedStepsKey looks for the {"verb":"create","create":{"steps":[...]}}
// nesting habit: exactly one non-verb key holding an object that contains a
// steps array. Returns the key when unambiguous, "" otherwise.
func nestedStepsKey(raw map[string]json.RawMessage) string {
	found := ""
	for k, v := range raw {
		if k == "verb" {
			continue
		}
		trimmed := bytes.TrimSpace(v)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(trimmed, &obj) != nil {
			continue
		}
		if inner, ok := obj["steps"]; ok && stepShapedArray(inner) {
			if found != "" {
				return "" // ambiguous: two wrapper candidates
			}
			found = k
		}
	}
	return found
}

// wrapperStepsConflict reports a wrapper object carrying a steps array when
// the top-level steps key is also present — two candidate lists, hard error
// rather than silently ignoring the wrapper's.
func wrapperStepsConflict(raw map[string]json.RawMessage) error {
	if raw == nil {
		return nil
	}
	if _, ok := raw["steps"]; !ok {
		return nil
	}
	if k := nestedStepsKey(raw); k != "" {
		return fmt.Errorf("plan: ambiguous: both 'steps' and wrapper %q supply step lists; pass the full list under 'steps' only", k)
	}
	return nil
}

// diagnoseCreateSteps builds the diagnostic error for a create call that
// carried no usable steps array, naming the keys actually received. When the
// payload nests steps under a single wrapper object, returns the wrapper key
// so the caller can unwrap instead of failing.
func diagnoseCreateSteps(raw map[string]json.RawMessage, argsJSON string) (unwrapKey string, err error) {
	if raw == nil {
		return "", nil
	}
	if k := nestedStepsKey(raw); k != "" {
		return k, nil
	}
	keys := planReceivedKeys(raw)
	var suspects []string
	for _, k := range keys {
		if k == "steps" {
			continue
		}
		if stepShapedArray(raw[k]) {
			suspects = append(suspects, k)
		}
	}
	if len(keys) == 0 {
		return "", nil // plain "no steps" case, handled by create()
	}
	msg := fmt.Sprintf("plan: create requires 'steps' (array of {id,title,note,checks}); received keys: %s", keyList(keys))
	if len(suspects) > 0 {
		s := suspects[0]
		if len(s) > maxDiagKeyChars {
			s = s[:maxDiagKeyChars] + "…"
		}
		msg += fmt.Sprintf(" — %s is not recognized; did you mean 'steps'?", s)
	}
	return "", fmt.Errorf("%s", msg)
}

// diagnoseUpdateUpdates builds the diagnostic for an update call without an
// updates array. Returns nil (old error path) when nothing diagnosable exists.
func diagnoseUpdateUpdates(raw map[string]json.RawMessage) error {
	if raw == nil {
		return nil
	}
	keys := planReceivedKeys(raw)
	if len(keys) == 0 || (len(keys) == 1 && keys[0] == "updates") {
		return nil
	}
	msg := fmt.Sprintf("plan: update requires 'updates' (array of {id,status,note}); received keys: %s", keyList(keys))
	for _, k := range keys {
		if k != "updates" && stepShapedArray(raw[k]) {
			s := k
			if len(s) > maxDiagKeyChars {
				s = s[:maxDiagKeyChars] + "…"
			}
			msg += fmt.Sprintf(" — %s is not recognized; did you mean 'updates'?", s)
			break
		}
	}
	return fmt.Errorf("%s", msg)
}

// ambiguousStepsConflict rejects a create call that supplies BOTH 'steps' and
// another step-shaped array under a different key: silently dropping the
// second list would hide half the model's intent.
func ambiguousStepsConflict(raw map[string]json.RawMessage, haveSteps bool) error {
	if raw == nil || !haveSteps {
		return nil
	}
	for k, v := range raw {
		if k == "verb" || k == "steps" || k == "description" || k == "reason" {
			continue
		}
		if stepShapedArray(v) {
			return fmt.Errorf("plan: ambiguous: both 'steps' and %q supply step lists; pass the full list under 'steps' only", k)
		}
	}
	return nil
}
