package loop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

const (
	maxPlanChecks           = 4
	maxPlanCheckIDChars     = 32
	maxPlanCheckDescChars   = 200
	maxPlanCheckToolChars   = 128
	maxPlanCheckArgsBytes   = 4096
	planCheckRenderMarker   = " || checks:"
	maxPlanCheckCallIDChars = 128
)

func clonePlanChecks(in []PlanCheck) []PlanCheck {
	if in == nil {
		return nil
	}
	out := make([]PlanCheck, len(in))
	for i, check := range in {
		out[i] = check
		out[i].Arguments = clonePlanArguments(check.Arguments)
	}
	return out
}

func cloneRevision(in *PlanRevision) *PlanRevision {
	if in == nil {
		return nil
	}
	return &PlanRevision{Reason: in.Reason, Summary: append([]string(nil), in.Summary...)}
}

func clonePlanSteps(in []PlanStep) []PlanStep {
	out := append([]PlanStep(nil), in...)
	for i := range out {
		out[i].Checks = clonePlanChecks(out[i].Checks)
	}
	return out
}

func clonePlanArguments(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	out, err := decodeJSONObject(raw)
	if err != nil {
		return nil
	}
	return out
}

func validatePlanChecks(in []planCheckArg) ([]PlanCheck, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxPlanChecks {
		return nil, fmt.Errorf("too many checks (%d > %d)", len(in), maxPlanChecks)
	}
	out := make([]PlanCheck, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, raw := range in {
		id := strings.TrimSpace(raw.ID)
		if id == "" || len([]rune(id)) > maxPlanCheckIDChars || strings.ContainsAny(id, "[]/") || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, fmt.Errorf("check[%d]: invalid id", i)
		}
		if seen[id] {
			return nil, fmt.Errorf("check[%d]: duplicate id %q", i, id)
		}
		seen[id] = true
		description := normalizePlanText(raw.Description)
		if description == "" {
			return nil, fmt.Errorf("check[%d].description: empty after trimming (max %d chars) — retryable: true", i, maxPlanCheckDescChars)
		}
		if len([]rune(description)) > maxPlanCheckDescChars {
			return nil, fmt.Errorf("check[%d].description: %d chars exceeds max %d — retryable: true", i, len([]rune(description)), maxPlanCheckDescChars)
		}
		tool := strings.TrimSpace(raw.Tool)
		if tool == "" || tool == "plan" || len([]rune(tool)) > maxPlanCheckToolChars {
			return nil, fmt.Errorf("check[%d]: invalid tool", i)
		}
		for _, r := range tool {
			if unicode.IsControl(r) {
				return nil, fmt.Errorf("check[%d]: invalid tool", i)
			}
		}
		args, err := decodeJSONObject(raw.Arguments)
		if err != nil {
			return nil, fmt.Errorf("check[%d]: arguments must be a JSON object: %w", i, err)
		}
		canonical, err := json.Marshal(args)
		if err != nil || len(canonical) > maxPlanCheckArgsBytes {
			return nil, fmt.Errorf("check[%d]: arguments too large", i)
		}
		out = append(out, PlanCheck{ID: id, Description: description, Tool: tool, Arguments: args, Status: PlanCheckPending})
	}
	return out, nil
}

func allPlanChecksPassed(step PlanStep) bool {
	for _, check := range step.Checks {
		if check.Status == PlanCheckBlocked {
			// Blocked = environment-denied, not missing evidence.
			continue
		}
		if check.Status != PlanCheckPassed {
			return false
		}
	}
	return true
}

// blockingChecksDetail renders every unpassed check of a step as
// id + the exact tool call that satisfies it, so a gating error carries its
// own recovery instructions instead of a bare refusal.
func blockingChecksDetail(step PlanStep) string {
	var parts []string
	for _, check := range step.Checks {
		if check.Status == PlanCheckPassed {
			continue
		}
		args, _ := json.Marshal(check.Arguments)
		parts = append(parts, fmt.Sprintf("%s: call %s with %s", check.ID, check.Tool, string(args)))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "; ")
}

func hasPlanChecks(p PlanState) bool {
	for _, step := range p.Steps {
		if len(step.Checks) > 0 {
			return true
		}
	}
	return false
}

func checkedPlanFits(p PlanState, maxChars int) bool {
	if !hasPlanChecks(p) && p.Revision == nil {
		return true
	}
	reserve := clonePlanState(p)
	// Reserve the longest statuses and bounded evidence references without
	// invoking the renderer's lossy overflow fallback for legacy plans.
	reserve.Version = 999999999
	for i := range reserve.Steps {
		step := &reserve.Steps[i]
		if strings.Contains(step.Title, planCheckRenderMarker) || strings.Contains(step.Note, planCheckRenderMarker) {
			return false
		}
		step.Status = StepInProgress
		for j := range step.Checks {
			step.Checks[j].Status = PlanCheckPending
			step.Checks[j].CallID = strings.Repeat("x", maxPlanCheckCallIDChars)
		}
	}
	// Both header counts may grow to the width of the total step count.
	size := len(planHeaderLine(reserve)) + 2*len(fmt.Sprint(len(reserve.Steps)))
	for _, step := range reserve.Steps {
		size += 1 + len(planStepLine(step))
	}
	if p.Revision != nil {
		if b, err := json.Marshal(p.Revision); err == nil {
			size += len(b) + len("[Plan revision: ]") + 1
		}
	}
	return size <= maxChars
}

func canonicalPlanArguments(raw []byte) ([]byte, error) {
	obj, err := decodeJSONObject(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty arguments")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON")
		}
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, errors.New("must be an object")
	}
	return obj, nil
}

func renderPlanChecks(checks []PlanCheck) string {
	if len(checks) == 0 {
		return ""
	}
	b, _ := json.Marshal(checks)
	return planCheckRenderMarker + string(b)
}

func parsePlanChecks(raw string) ([]PlanCheck, error) {
	var in []struct {
		ID          string          `json:"id"`
		Description string          `json:"description"`
		Tool        string          `json:"tool"`
		Arguments   json.RawMessage `json:"arguments"`
		Status      PlanCheckStatus `json:"status"`
		CallID      string          `json:"call_id"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, err
	}
	args := make([]planCheckArg, len(in))
	for i, check := range in {
		if check.Status != "" && check.Status != PlanCheckPending && check.Status != PlanCheckPassed && check.Status != PlanCheckFailed && check.Status != PlanCheckBlocked {
			return nil, fmt.Errorf("check[%d]: unknown status", i)
		}
		if len([]rune(check.CallID)) > maxPlanCheckCallIDChars {
			return nil, fmt.Errorf("check[%d]: call id too long", i)
		}
		for _, r := range check.CallID {
			if unicode.IsControl(r) {
				return nil, fmt.Errorf("check[%d]: invalid call id", i)
			}
		}
		args[i] = planCheckArg{ID: check.ID, Description: check.Description, Tool: check.Tool, Arguments: check.Arguments}
	}
	out, err := validatePlanChecks(args)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Status = in[i].Status
		if out[i].Status == "" {
			out[i].Status = PlanCheckPending
		}
		out[i].CallID = in[i].CallID
	}
	return out, nil
}

func (s *PlanStore) CheckEpoch() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch
}

func (s *PlanStore) MatchesCheck(tool, args string) bool {
	canonical, err := canonicalPlanArguments([]byte(args))
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, step := range s.planStepsLocked() {
		for _, check := range step.Checks {
			if check.Tool == tool && samePlanArguments(check.Arguments, canonical) {
				return true
			}
		}
	}
	return false
}

func samePlanArguments(args map[string]any, canonical []byte) bool {
	b, err := json.Marshal(args)
	return err == nil && bytes.Equal(b, canonical)
}

func (s *PlanStore) RecordCheckOutcome(epoch uint64, tool, args, callID string, failed bool) {
	canonical, err := canonicalPlanArguments([]byte(args))
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch || s.plan == nil {
		return
	}
	callID = truncatePlanCallID(callID)
	status := PlanCheckPassed
	if failed {
		status = PlanCheckFailed
	}
	changed := false
	for i := range s.plan.Steps {
		for j := range s.plan.Steps[i].Checks {
			check := &s.plan.Steps[i].Checks[j]
			if check.Tool == tool && samePlanArguments(check.Arguments, canonical) {
				if check.Status != status || check.CallID != callID {
					check.Status, check.CallID = status, callID
					changed = true
				}
				if failed && s.plan.Steps[i].Status == StepDone {
					s.plan.Steps[i].Status = StepInProgress
					changed = true
				}
			}
		}
	}
	if changed {
		s.plan.Version++
		s.notifyLocked(false, false)
	}
}

// RecordCheckDenied transitions a matching check to blocked after the
// environment refused to run it (approval or config denial). Blocked checks
// stop gating completion and stop counting as missing evidence.
func (s *PlanStore) RecordCheckDenied(epoch uint64, tool, args, callID string) {
	canonical, err := canonicalPlanArguments([]byte(args))
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch || s.plan == nil {
		return
	}
	callID = truncatePlanCallID(callID)
	changed := false
	for i := range s.plan.Steps {
		for j := range s.plan.Steps[i].Checks {
			check := &s.plan.Steps[i].Checks[j]
			if check.Tool == tool && samePlanArguments(check.Arguments, canonical) {
				if check.Status != PlanCheckBlocked || check.CallID != callID {
					check.Status, check.CallID = PlanCheckBlocked, callID
					changed = true
				}
			}
		}
	}
	if changed {
		s.plan.Version++
		s.notifyLocked(false, false)
	}
}

func truncatePlanCallID(callID string) string {
	if callID != "" {
		valid := len(callID) <= maxPlanCheckCallIDChars
		for _, r := range callID {
			switch {
			case r == '-', r == '_', r == '.', r == ':', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			default:
				valid = false
			}
		}
		if valid {
			return callID
		}
	}
	digest := sha256.Sum256([]byte(callID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (s *PlanStore) InvalidateChecks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return
	}
	changed := false
	working := clonePlanSteps(s.plan.Steps)
	for i := range working {
		if len(working[i].Checks) == 0 {
			continue
		}
		if working[i].Status == StepDone {
			working[i].Status = StepInProgress
			changed = true
		}
		for j := range working[i].Checks {
			if working[i].Checks[j].Status != PlanCheckPending || working[i].Checks[j].CallID != "" {
				changed = true
			}
			working[i].Checks[j].Status = PlanCheckPending
			working[i].Checks[j].CallID = ""
		}
	}
	if !changed {
		return
	}
	s.noteStatusTransitionsLocked(s.plan.Steps, working)
	s.plan = &PlanState{Version: s.nextVersion(), Steps: working, Revision: cloneRevision(s.plan.Revision)}
	s.notifyLocked(false, false)
}

func (s *PlanStore) PendingChecks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, step := range s.planStepsLocked() {
		for _, check := range step.Checks {
			if check.Status != PlanCheckPassed && check.Status != PlanCheckBlocked {
				out = append(out, step.ID+"/"+check.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *PlanStore) planStepsLocked() []PlanStep {
	if s.plan == nil {
		return nil
	}
	return s.plan.Steps
}
