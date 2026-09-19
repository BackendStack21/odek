package loop

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

const (
	maxRevisionReason      = 240
	maxRevisionOps         = 8
	maxRevisionSummary     = 8
	maxRevisionSummaryItem = 2048
	planRevisionPrefix     = "[Plan revision:"
)

type planRevisionArgs struct {
	Verb       string           `json:"verb"`
	Reason     string           `json:"reason"`
	Operations []planRevisionOp `json:"operations"`
}
type planRevisionOp struct {
	Kind          string         `json:"kind"`
	StepID        string         `json:"step_id"`
	AfterID       string         `json:"after_id"`
	BeforeID      string         `json:"before_id"`
	Title         *string        `json:"title"`
	Note          *string        `json:"note"`
	Steps         planStepList   `json:"steps"`
	CarryChecksTo string         `json:"carry_checks_to"`
	Checks        []planCheckArg `json:"checks"`
}

func preserveCheckedPlan(old PlanState, next *PlanState) error {
	for _, previous := range old.Steps {
		if len(previous.Checks) == 0 {
			continue
		}
		idx := indexOfStep(next.Steps, previous.ID)
		if idx < 0 {
			return fmt.Errorf("plan: create cannot remove or change checked step %q; use revise", previous.ID)
		}
		step := &next.Steps[idx]
		for _, check := range previous.Checks {
			found := false
			for j, c := range step.Checks {
				if c.ID != check.ID {
					continue
				}
				if c.Tool != check.Tool || c.Description != check.Description || !samePlanArguments(c.Arguments, mustCanonical(check.Arguments)) {
					return fmt.Errorf("plan: create cannot remove or change checked requirement %s/%s; use revise", previous.ID, check.ID)
				}
				step.Checks[j].Status, step.Checks[j].CallID = check.Status, check.CallID
				found = true
			}
			if !found {
				return fmt.Errorf("plan: create cannot remove or change checked requirement %s/%s; use revise", previous.ID, check.ID)
			}
		}
		step.Status = previous.Status
		if step.Title != previous.Title {
			reopenRevisedStep(step)
		}
		if step.Status == StepDone && !allPlanChecksPassed(*step) {
			step.Status = StepInProgress
		}
	}
	next.Revision = cloneRevision(old.Revision)
	return nil
}

func reopenRevisedStep(step *PlanStep) {
	if step.Status == StepDone || step.Status == StepBlocked {
		step.Status = StepInProgress
	}
	for i := range step.Checks {
		step.Checks[i].Status = PlanCheckPending
		step.Checks[i].CallID = ""
	}
}

func mustCanonical(args map[string]any) []byte { b, _ := json.Marshal(args); return b }
func (s *PlanStore) revise(raw string) (string, error) {
	var args planRevisionArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("plan: revise: %w", err)
	}
	if strings.TrimSpace(args.Reason) == "" || len([]rune(args.Reason)) > maxRevisionReason {
		return "", fmt.Errorf("plan: revise requires a bounded reason")
	}
	if len(args.Operations) == 0 || len(args.Operations) > maxRevisionOps {
		return "", fmt.Errorf("plan: revise requires 1..%d operations", maxRevisionOps)
	}
	if s.plan == nil {
		return "", fmt.Errorf("plan: revise: no active plan")
	}
	working := clonePlanSteps(s.plan.Steps)
	var summary []string
	for _, op := range args.Operations {
		switch op.Kind {
		case "add":
			steps, err := validateRevisionSteps(op.Steps, working)
			if err != nil {
				return "", err
			}
			pos, err := revisionPosition(working, op.AfterID, op.BeforeID)
			if err != nil {
				return "", err
			}
			working = append(append(append([]PlanStep{}, working[:pos]...), steps...), working[pos:]...)
			names := make([]string, 0, len(steps))
			for _, step := range steps {
				names = append(names, step.ID)
			}
			summary = append(summary, fmt.Sprintf("add %q after %q before %q", names, op.AfterID, op.BeforeID))
		case "edit":
			idx := indexOfStep(working, op.StepID)
			if idx < 0 {
				return "", fmt.Errorf("plan: revise: unknown step %q", op.StepID)
			}
			if op.Title != nil {
				title := normalizePlanText(*op.Title)
				if title == "" {
					return "", fmt.Errorf("plan: revise: title is required")
				}
				if title != working[idx].Title {
					working[idx].Title = title
					reopenRevisedStep(&working[idx])
				}
			}
			if op.Note != nil {
				working[idx].Note = normalizePlanText(*op.Note)
			}
			if len(op.Checks) > 0 {
				checks, err := validatePlanChecks(op.Checks)
				if err != nil {
					return "", err
				}
				working[idx].Checks = append(clonePlanChecks(working[idx].Checks), checks...)
				if working[idx].Status == StepDone {
					working[idx].Status = StepInProgress
				}
				summary = append(summary, fmt.Sprintf("edit checks %q", op.StepID))
			} else {
				summary = append(summary, fmt.Sprintf("edit %q", op.StepID))
			}
		case "move":
			idx := indexOfStep(working, op.StepID)
			if idx < 0 {
				return "", fmt.Errorf("plan: revise: unknown step %q", op.StepID)
			}
			step := working[idx]
			working = append(working[:idx], working[idx+1:]...)
			pos, err := revisionPosition(working, op.AfterID, op.BeforeID)
			if err != nil {
				return "", err
			}
			working = append(working, PlanStep{})
			copy(working[pos+1:], working[pos:])
			working[pos] = step
			summary = append(summary, fmt.Sprintf("move %q after %q before %q", op.StepID, op.AfterID, op.BeforeID))
		case "split", "supersede":
			idx := indexOfStep(working, op.StepID)
			if idx < 0 {
				return "", fmt.Errorf("plan: revise: unknown step %q", op.StepID)
			}
			// Reserve every existing id except the replaced step's own — a
			// superseded step's id may legitimately be reused by a replacement,
			// all others must stay collision-free for auto-assigned ids.
			replaced := working[idx].ID
			reserved := make([]PlanStep, 0, len(working))
			for _, st := range working {
				if st.ID != replaced {
					reserved = append(reserved, st)
				}
			}
			steps, err := validateRevisionSteps(op.Steps, reserved)
			if err != nil {
				return "", err
			}
			if op.Kind == "split" && len(steps) < 2 {
				return "", fmt.Errorf("plan: split requires at least two replacement steps")
			}
			if len(working[idx].Checks) > 0 {
				if op.CarryChecksTo == "" {
					return "", fmt.Errorf("plan: revise: carry_checks_to required")
				}
				target := -1
				for i := range steps {
					if steps[i].ID == op.CarryChecksTo {
						target = i
					}
				}
				if target < 0 {
					return "", fmt.Errorf("plan: revise: carry_checks_to must name a replacement")
				}
				for _, existing := range steps[target].Checks {
					for _, carried := range working[idx].Checks {
						if existing.ID == carried.ID {
							return "", fmt.Errorf("plan: duplicate carried check %q", existing.ID)
						}
					}
				}
				steps[target].Checks = append(steps[target].Checks, clonePlanChecks(working[idx].Checks)...)
				steps[target].Status = StepInProgress
				for j := range steps[target].Checks {
					steps[target].Checks[j].Status = PlanCheckPending
					steps[target].Checks[j].CallID = ""
				}
			}
			working = append(append(append([]PlanStep{}, working[:idx]...), steps...), working[idx+1:]...)
			names := make([]string, 0, len(steps))
			for _, step := range steps {
				names = append(names, step.ID)
			}
			summary = append(summary, fmt.Sprintf("%s %q -> %q; checks to %q", op.Kind, op.StepID, names, op.CarryChecksTo))
		default:
			return "", fmt.Errorf("plan: revise: unknown operation %q", op.Kind)
		}
	}
	if err := validateRevisionFinalSteps(working, s.maxSteps); err != nil {
		return "", err
	}
	if reflect.DeepEqual(working, s.plan.Steps) {
		return s.renderLocked(), nil
	}
	candidate := PlanState{Version: s.nextVersion(), Steps: working, Revision: &PlanRevision{Reason: normalizePlanText(args.Reason), Summary: summary}}
	if !validPlanRevision(candidate.Revision) || !checkedPlanFits(candidate, s.maxRenderChars) {
		return "", fmt.Errorf("plan: revise exceeds configured limits")
	}
	s.noteStatusTransitionsLocked(s.plan.Steps, working)
	s.plan = &candidate
	s.epoch++
	s.revisionNotify = true
	return s.renderLocked(), nil
}

func validateRevisionFinalSteps(steps []PlanStep, max int) error {
	if len(steps) == 0 || len(steps) > max {
		return fmt.Errorf("plan: revise exceeds step cap")
	}
	seen := map[string]bool{}
	for _, step := range steps {
		if step.ID == "" || len(step.ID) > maxPlanIDChars || seen[step.ID] || strings.ContainsAny(step.ID, "[]") || strings.ContainsFunc(step.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return fmt.Errorf("plan: revise: invalid or duplicate step id %q", step.ID)
		}
		seen[step.ID] = true
		if step.Title == "" || len(step.Title) > maxPlanTitleChars {
			return fmt.Errorf("plan: revise: invalid title for %q", step.ID)
		}
		args := make([]planCheckArg, len(step.Checks))
		for i, c := range step.Checks {
			args[i] = planCheckArg{ID: c.ID, Description: c.Description, Tool: c.Tool, Arguments: mustCanonical(c.Arguments)}
		}
		if _, err := validatePlanChecks(args); err != nil {
			return fmt.Errorf("plan: revise: step %q: %w", step.ID, err)
		}
	}
	return nil
}

// validateRevisionSteps validates the new steps of an add/split/supersede
// operation. working carries the ids already present in the plan so
// auto-assigned ids cannot collide with existing steps.
func validateRevisionSteps(in []planStepArg, working []PlanStep) ([]PlanStep, error) {
	reserved := make(map[string]bool, len(working))
	for _, st := range working {
		reserved[st.ID] = true
	}
	in = fillAutoStepIDs(in, reserved)
	if len(in) == 0 {
		return nil, fmt.Errorf("plan: revise: steps must not be empty")
	}
	out := make([]PlanStep, 0, len(in))
	for _, step := range in {
		checks, err := validatePlanChecks(step.Checks)
		if err != nil {
			return nil, err
		}
		out = append(out, PlanStep{ID: strings.TrimSpace(step.ID), Title: normalizePlanText(step.Title), Note: normalizePlanText(step.Note), Status: StepPending, Checks: checks})
	}
	if err := validateRevisionFinalSteps(out, extractPlanStepCap); err != nil {
		return nil, err
	}
	return out, nil
}

func validPlanRevision(rev *PlanRevision) bool {
	if rev == nil || strings.TrimSpace(rev.Reason) == "" || len([]rune(rev.Reason)) > maxRevisionReason || len(rev.Summary) == 0 || len(rev.Summary) > maxRevisionSummary {
		return false
	}
	for _, item := range rev.Summary {
		if item == "" || len([]rune(item)) > maxRevisionSummaryItem || strings.ContainsAny(item, "\r\n") {
			return false
		}
	}
	return true
}

// revisionPosition permits either relative anchor, including insertion before
// the first step. An omitted anchor appends at the end.
func revisionPosition(steps []PlanStep, after, before string) (int, error) {
	if after != "" && before != "" {
		return 0, fmt.Errorf("plan: revise: choose either after_id or before_id")
	}
	if after != "" {
		if i := indexOfStep(steps, after); i >= 0 {
			return i + 1, nil
		}
		return 0, fmt.Errorf("plan: revise: unknown after_id %q", after)
	}
	if before != "" {
		if i := indexOfStep(steps, before); i >= 0 {
			return i, nil
		}
		return 0, fmt.Errorf("plan: revise: unknown before_id %q", before)
	}
	return len(steps), nil
}
