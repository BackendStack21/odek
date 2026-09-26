package loop

// Structured plan state for the agent loop (docs/PLANNING.md).
//
// The model maintains an advisory plan through one built-in `plan` tool.
// State lives in a PlanStore shared between the tool and the engine (the
// memory-tool pattern: one manager object, two holders). The engine renders
// the state into a prefix-recognized protected system message — same
// recognize / protect / upsert / survive-restart treatment as the rolling
// compaction digest — so the decomposition survives context trimming and
// process restarts (`odek continue` re-parses it from the transcript).
//
// Validation is fail-closed: any malformed input rejects the whole call with
// a typed error and leaves the state untouched. Any status transition is
// allowed for unchecked steps; checked steps require successful tool outcomes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/session"
)

// ── Types ─────────────────────────────────────────────────────────────

// StepStatus is the lifecycle state of one plan step.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepDone       StepStatus = "done"
	StepBlocked    StepStatus = "blocked"
)

// validStepStatus reports whether s is one of the four known statuses.
func validStepStatus(s StepStatus) bool {
	switch s {
	case StepPending, StepInProgress, StepDone, StepBlocked:
		return true
	}
	return false
}

// PlanStep is one unit of planned work. IDs are model-chosen short tokens
// (e.g. "s1"); they exist so updates can target steps without positional
// ambiguity when the list is reordered.
type PlanStep struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Status StepStatus  `json:"status"`
	Note   string      `json:"note,omitempty"`
	Checks []PlanCheck `json:"checks,omitempty"`
}

type PlanCheckStatus string

const (
	PlanCheckPending PlanCheckStatus = "pending"
	PlanCheckPassed  PlanCheckStatus = "passed"
	PlanCheckFailed  PlanCheckStatus = "failed"
	// PlanCheckBlocked marks a check the environment refused (approval or
	// config denial). Blocked checks do not gate step completion; they stay
	// visible for closeout honesty (coverage: 2/3, 1 blocked).
	PlanCheckBlocked PlanCheckStatus = "blocked"
)

type PlanCheck struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Tool        string          `json:"tool"`
	Arguments   map[string]any  `json:"arguments"`
	Status      PlanCheckStatus `json:"status"`
	CallID      string          `json:"call_id,omitempty"`
}

// PlanState is the authoritative plan. Version bumps on every mutation and
// is echoed in the rendered message so drift is correlatable.
type PlanState struct {
	Version  int           `json:"version"`
	Steps    []PlanStep    `json:"steps"`
	Revision *PlanRevision `json:"revision,omitempty"`
}

type PlanRevision struct {
	Reason  string   `json:"reason"`
	Summary []string `json:"summary"`
}

// PlanChange describes one effective plan mutation for the change
// notification path (see PlanStore.SetOnChange). It carries aggregate
// counts and the new version ONLY — never step titles or notes — so it can
// be mapped straight onto the minimality-constrained odek.event/v1 stream
// (plan_created / plan_updated).
type PlanChange struct {
	Created       bool // true when the mutation was a create verb (wholesale replace)
	Steps         int  // total step count after the mutation
	Done          int
	InProgress    int
	Blocked       int
	Pending       int
	Version       int  // store version after the mutation
	BlockedStreak bool // true when this mutation tripped the 3-blocked streak
	Revised       bool
}

// Structural caps enforced by validation (docs/PLANNING.md — Fail-Closed
// Validation). Sizes bound the rendered message so a hostile or careless
// plan cannot blow up the prompt.
const (
	maxPlanIDChars    = 32
	maxPlanTitleChars = 200
)

// Fallback caps for NewPlanStore when the caller passes degenerate values.
// Mirrors the config-layer defaults (internal/config DefaultPlanningConfig);
// duplicated here because internal/loop must not import internal/config.
const (
	defaultPlanMaxSteps       = 12
	defaultPlanMaxRenderChars = 2000
)

// ── Store ─────────────────────────────────────────────────────────────

// PlanStore holds the engine's plan behind a dedicated mutex: plan calls can
// arrive inside a parallel tool batch (max_tool_parallel defaults to 4), so
// every mutation must serialize. Caps come from resolved config values —
// never raw project config.
type PlanStore struct {
	mu                  sync.Mutex
	plan                *PlanState // nil until first plan(create)
	maxSteps            int
	maxRenderChars      int
	onChange            func(PlanChange)         // optional; fired under mu after each effective mutation
	onValidationFailure func(verb, class string) // optional; fired once per rejected call (see SetOnValidationFailure)
	blockedStreak       int                      // consecutive blocked status transitions
	lastBlocked         bool                     // last status transition was to blocked
	blockedFired        bool                     // this mutation tripped the streak (consumed by notify)
	epoch               uint64
	revisionNotify      bool
}

// NewPlanStore creates a store with the given resolved caps. Degenerate
// values fall back to the defaults above.
func NewPlanStore(maxSteps, maxRenderChars int) *PlanStore {
	if maxSteps < 1 {
		maxSteps = defaultPlanMaxSteps
	}
	if maxRenderChars < 1 {
		maxRenderChars = defaultPlanMaxRenderChars
	}
	return &PlanStore{maxSteps: maxSteps, maxRenderChars: maxRenderChars}
}

// Snapshot returns a copy of the current plan (ok=false when none exists).
func (s *PlanStore) Snapshot() (PlanState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return PlanState{}, false
	}
	return clonePlanState(*s.plan), true
}

// LastTransitionBlocked reports whether the most recent status transition
// was to blocked (reset by create / done / in_progress). Used by the stall
// hint to escalate "stop retrying that class".
func (s *PlanStore) LastTransitionBlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBlocked
}

// Restore replaces the state wholesale (restart-resume path). The caller
// owns validation — see parsePlanState.
func (s *PlanStore) Restore(st PlanState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := clonePlanState(st)
	restoredChecks := false
	for i := range cp.Steps {
		checked := len(cp.Steps[i].Checks) > 0
		for j := range cp.Steps[i].Checks {
			if cp.Steps[i].Checks[j].Status != PlanCheckPending || cp.Steps[i].Status == StepDone {
				restoredChecks = true
			}
			cp.Steps[i].Checks[j].Status = PlanCheckPending
			cp.Steps[i].Checks[j].CallID = ""
		}
		if checked && cp.Steps[i].Status == StepDone {
			cp.Steps[i].Status = StepInProgress
			restoredChecks = true
		}
	}
	if restoredChecks {
		cp.Version++
	}
	s.plan = &cp
	s.epoch++
	s.blockedStreak = 0
	s.lastBlocked = false
	s.blockedFired = false
	s.revisionNotify = false
}

func clonePlanState(st PlanState) PlanState {
	st.Steps = append([]PlanStep(nil), st.Steps...)
	if st.Revision != nil {
		st.Revision = &PlanRevision{Reason: st.Revision.Reason, Summary: append([]string(nil), st.Revision.Summary...)}
	}
	for i := range st.Steps {
		st.Steps[i].Checks = clonePlanChecks(st.Steps[i].Checks)
	}
	return st
}

// Reset clears the state (run start with no persisted plan).
func (s *PlanStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = nil
	s.epoch++
	s.blockedStreak = 0
	s.lastBlocked = false
	s.blockedFired = false
}

// SetOnChange registers an optional callback fired exactly once per
// effective mutation (create, or update/complete that bumped the version).
// Idempotent no-ops and the read-only get verb never fire it; Restore and
// Reset are resume-path bookkeeping, not model actions, and never fire it.
//
// The engine registers its event emitter here at SetPlanStore time — the
// store knows WHEN a mutation happened, the engine owns HOW it reaches the
// odek.event/v1 stream. The callback is invoked while the store mutex is
// held so notification order always matches mutation (== version) order even
// when parallel tool batches race: fn must therefore be non-blocking and
// must not call back into the PlanStore.
func (s *PlanStore) SetOnChange(fn func(PlanChange)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// ── Tool-call envelope ────────────────────────────────────────────────

type planStepArg struct {
	ID     string         `json:"id"`
	Title  string         `json:"title"`
	Note   string         `json:"note"`
	Checks []planCheckArg `json:"checks"`
}

type planCheckArg struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Tool        string          `json:"tool"`
	Arguments   json.RawMessage `json:"arguments"`
}

type planUpdateArg struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
	Note   string `json:"note,omitempty"`
}

type planArgs struct {
	Verb    string          `json:"verb"`
	Steps   planStepList    `json:"steps,omitempty"`
	Updates []planUpdateArg `json:"updates,omitempty"`
	StepID  string          `json:"step_id,omitempty"`

	// check_replace only: replace one dead/stale check with a fresh one,
	// or mark it satisfied by equivalent evidence. Justification is
	// mandatory — the replacement is audit-trailed via the revision
	// mechanism.
	CheckID       string         `json:"check_id,omitempty"`
	Justification string         `json:"justification,omitempty"`
	Replacement   *planCheckArg `json:"replacement,omitempty"`
	EvidenceNote  string         `json:"evidence_note,omitempty"`
}

// Execute runs one plan tool call (the full argument envelope) and returns
// the model-facing result. Serialized internally; safe inside parallel batches.
// Every effective mutation fires the OnChange callback exactly once per call
// — never per-step within an atomic batch.
func (s *PlanStore) Execute(argsJSON string) (string, error) {
	res, err := s.executeArgs(argsJSON)
	if err != nil {
		s.mu.Lock()
		fn := s.onValidationFailure
		s.mu.Unlock()
		if fn != nil {
			fn(verbFromArgs(argsJSON), classifyPlanFailure(err))
		}
	}
	return res, err
}

func (s *PlanStore) executeArgs(argsJSON string) (string, error) {
	var args planArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("plan: parse args: %w", err)
	}
	raw := planRawEnvelope(argsJSON)
	var inference string
	// Argument-resilience layer: diagnostics before the typed switch, so a
	// malformed envelope fails with the keys it actually carried. Every path
	// below either returns a diagnostic error or proceeds into the same
	// fail-closed validation as before — leniency is limited to shapes that
	// are unambiguous (missing ids, string steps, a single steps wrapper).
	if args.Verb == "" {
		if keys := planReceivedKeys(raw); len(keys) > 0 {
			return "", fmt.Errorf("plan: unknown verb \"\" (want create/update/complete/revise/check_replace/get); received keys: %s — set \"verb\" to one of the six", keyList(keys))
		}
		return "", fmt.Errorf("plan: unknown verb %q (want create/update/complete/revise/check_replace/get)", args.Verb)
	}
	// Field-name aliases (expert-review restricted): leniency is name-level
	// only, never shape-level. complete accepts "id" for step_id; update
	// accepts a single-step form via step_id. Conflicts are hard errors —
	// a silent pick would hide half the model's intent.
	if args.Verb == "complete" && args.StepID == "" {
		if idRaw, ok := raw["id"]; ok {
			var id string
			if json.Unmarshal(idRaw, &id) == nil && id != "" {
				args.StepID = id
				inference = "plan: accepted \"id\" as alias for step_id on complete — use step_id next time"
			}
		}
		if args.StepID == "" {
			return "", teaching("complete", fmt.Sprintf("plan: complete requires 'step_id' (the step to mark done); received keys: %s", keyList(planReceivedKeys(raw))))
		}
	}
	if args.Verb == "update" {
		if sidRaw, hasSID := raw["step_id"]; hasSID {
			var sid string
			_ = json.Unmarshal(sidRaw, &sid)
			switch {
			case len(args.Updates) == 0:
				u := planUpdateArg{ID: sid}
				if raw["status"] != nil {
					_ = json.Unmarshal(raw["status"], &u.Status)
				}
				if raw["note"] != nil {
					_ = json.Unmarshal(raw["note"], &u.Note)
				}
				args.Updates = []planUpdateArg{u}
				inference = `plan: accepted single-step form (step_id) — prefer {"verb":"update","updates":[...]} next time`
			case len(args.Updates) > 1:
				return "", teaching("update", "plan: step_id cannot be combined with multiple updates — send updates only")
			default:
				if args.Updates[0].ID != sid {
					return "", teaching("update", fmt.Sprintf("plan: step_id %q conflicts with updates[0].id %q — they must agree", sid, args.Updates[0].ID))
				}
			}
		}
	}
	if args.Verb == "create" {
		// Ambiguity gates on KEY PRESENCE, not decoded length: "steps":[] or
		// "steps":null still means the model used the canonical field, so a
		// competing list under another key is a conflict worth reporting.
		_, stepsKey := raw["steps"]
		if err := ambiguousStepsConflict(raw, stepsKey); err != nil {
			return "", err
		}
		if err := wrapperStepsConflict(raw); err != nil {
			return "", err
		}
		if len(args.Steps) == 0 && !stepsKey {
			unwrapKey, derr := diagnoseCreateSteps(raw, argsJSON)
			if derr != nil {
				return "", derr
			}
			if unwrapKey != "" {
				var obj map[string]json.RawMessage
				if json.Unmarshal(raw[unwrapKey], &obj) == nil {
					if inner, ok := obj["steps"]; ok {
						if err := json.Unmarshal(inner, &args.Steps); err != nil {
							return "", fmt.Errorf("plan: parse args: %w", err)
						}
						inference = fmt.Sprintf("plan: inferred steps from the %q wrapper object — nest steps at the top level next time", unwrapKey)
					}
				}
			}
		}
	}
	if args.Verb == "update" && len(args.Updates) == 0 {
		if derr := diagnoseUpdateUpdates(raw); derr != nil {
			return "", teaching("update", derr.Error())
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prevVersion := 0
	if s.plan != nil {
		prevVersion = s.plan.Version
	}
	var res string
	var err error
	s.blockedFired = false
	s.revisionNotify = false
	switch args.Verb {
	case "create":
		res, err = s.create(args.Steps)
	case "update":
		res, err = s.update(args.Updates)
	case "complete":
		res, err = s.complete(args.StepID)
	case "revise":
		res, err = s.revise(argsJSON)
		if err != nil {
			err = teaching("revise", err.Error())
		}
	case "check_replace":
		res, err = s.checkReplace(args)
		if err != nil {
			err = teaching("check_replace", err.Error())
		}
	case "get":
		return s.get()
	default:
		return "", fmt.Errorf("plan: unknown verb %q (want create/update/complete/revise/check_replace/get)", args.Verb)
	}
	// A version bump is exactly the "effective mutation" contract: no-op
	// update/complete calls return early without reassigning s.plan, so they
	// stay silent. create always bumps (fresh state), so it maps onto
	// plan_created; every other bumping mutation is plan_updated.
	if err == nil && s.plan != nil && s.plan.Version != prevVersion {
		s.notifyLocked(args.Verb == "create", s.blockedFired)
	}
	s.revisionNotify = false
	if inference != "" && err == nil {
		res = inference + "\n" + res
	}
	return res, err
}

// notifyLocked snapshots the post-mutation state into a PlanChange and fires
// the change callback. Caller holds s.mu (see SetOnChange for the contract).
func (s *PlanStore) notifyLocked(created, blockedStreak bool) {
	if s.onChange == nil || s.plan == nil {
		return
	}
	ch := PlanChange{
		Created:       created,
		Steps:         len(s.plan.Steps),
		Version:       s.plan.Version,
		BlockedStreak: blockedStreak,
		Revised:       s.revisionNotify,
	}
	for _, st := range s.plan.Steps {
		switch st.Status {
		case StepDone:
			ch.Done++
		case StepInProgress:
			ch.InProgress++
		case StepBlocked:
			ch.Blocked++
		default:
			ch.Pending++
		}
	}
	s.onChange(ch)
	s.revisionNotify = false
}

// nextVersion returns the version the next successful mutation gets:
// 1 for a fresh plan, current version + 1 otherwise.
func (s *PlanStore) nextVersion() int {
	if s.plan == nil {
		return 1
	}
	return s.plan.Version + 1
}

// renderLocked renders the current plan. Caller holds s.mu.
func (s *PlanStore) renderLocked() string {
	return renderPlan(*s.plan, s.maxRenderChars)
}

func (s *PlanStore) create(steps []planStepArg) (string, error) {
	steps = fillAutoStepIDs(steps, nil)
	if len(steps) < 1 || len(steps) > s.maxSteps {
		return "", fmt.Errorf("plan: create wants 1..%d steps, got %d", s.maxSteps, len(steps))
	}
	out := make([]PlanStep, 0, len(steps))
	seen := make(map[string]bool, len(steps))
	for i, in := range steps {
		id := strings.TrimSpace(in.ID)
		switch {
		case id == "":
			return "", fmt.Errorf("plan: step[%d]: id is required", i)
		case len(id) > maxPlanIDChars:
			return "", fmt.Errorf("plan: step[%d]: id is too long (%d > %d chars)", i, len(id), maxPlanIDChars)
		case strings.ContainsAny(id, " \t\n\r[]"):
			return "", fmt.Errorf("plan: step[%d]: id %q must be a short token without whitespace or brackets", i, id)
		case seen[id]:
			return "", fmt.Errorf("plan: step[%d]: duplicate step id %q", i, id)
		}
		seen[id] = true
		title := normalizePlanText(in.Title)
		if title == "" {
			return "", fmt.Errorf("plan: steps[%d].title: required and empty after trimming — retryable: true", i)
		}
		if len(title) > maxPlanTitleChars {
			return "", fmt.Errorf("plan: step[%d]: title is too long (%d > %d chars)", i, len(title), maxPlanTitleChars)
		}
		checks, err := validatePlanChecks(in.Checks)
		if err != nil {
			return "", fmt.Errorf("plan: step[%d]: %w", i, err)
		}
		out = append(out, PlanStep{ID: id, Title: title, Status: StepPending, Note: normalizePlanText(in.Note), Checks: checks})
	}
	candidate := PlanState{Version: s.nextVersion(), Steps: out}
	if s.plan != nil && hasPlanChecks(*s.plan) {
		preserveErr := preserveCheckedPlan(*s.plan, &candidate)
		if preserveErr != nil {
			// create may always reset: incompatible checked plans are
			// superseded, not refused. The supersession is audit-trailed
			// in the revision block so no verification history silently
			// disappears.
			candidate.Revision = archivedPlanRevision(*s.plan)
		}
	}
	if !checkedPlanFits(candidate, s.maxRenderChars) {
		return "", fmt.Errorf("plan: checked plan exceeds max_render_chars (%d) or uses reserved checks delimiter in title/note", s.maxRenderChars)
	}
	s.epoch++
	s.blockedStreak = 0
	s.lastBlocked = false
	s.blockedFired = false
	s.plan = &candidate
	return s.renderLocked(), nil
}

func (s *PlanStore) update(updates []planUpdateArg) (string, error) {
	if len(updates) == 0 {
		return "", errors.New("plan: update: no updates given")
	}
	// Apply to a working copy first: any invalid entry rejects the whole
	// call and leaves the stored plan untouched (atomic batch).
	var working []PlanStep
	if s.plan != nil {
		working = clonePlanSteps(s.plan.Steps)
	}
	changed := false
	for i, u := range updates {
		idx := indexOfStep(working, strings.TrimSpace(u.ID))
		if idx < 0 {
			return "", fmt.Errorf("plan: update: unknown step id %q", u.ID)
		}
		if u.Status != "" {
			st := StepStatus(u.Status)
			if !validStepStatus(st) {
				return "", fmt.Errorf("plan: update: step[%d]: unknown status %q", i, u.Status)
			}
			if working[idx].Status != st {
				if st == StepDone && !allPlanChecksPassed(working[idx]) {
					return "", fmt.Errorf("plan: update: step %q has checks that have not passed — retryable: true; blocking checks and satisfying calls: %s", working[idx].ID, blockingChecksDetail(working[idx]))
				}
				working[idx].Status = st
				changed = true
			}
		}
		if u.Note != "" {
			note := normalizePlanText(u.Note)
			if working[idx].Note != note {
				working[idx].Note = note
				changed = true
			}
		}
	}
	if !changed {
		// Status already terminal-equal — allowed, idempotent, no version bump.
		return s.renderLocked(), nil
	}
	var old []PlanStep
	if s.plan != nil {
		old = s.plan.Steps
	}
	candidate := PlanState{Version: s.nextVersion(), Steps: working, Revision: cloneRevision(s.plan.Revision)}
	if !checkedPlanFits(candidate, s.maxRenderChars) {
		return "", fmt.Errorf("plan: checked plan exceeds max_render_chars (%d) or uses reserved checks delimiter in title/note", s.maxRenderChars)
	}
	s.noteStatusTransitionsLocked(old, working)
	s.plan = &candidate
	return s.renderLocked(), nil
}

// checkReplace replaces one declared check — either with a fresh pending
// check (replacement) or with satisfied-by-equivalent-evidence (evidence_note).
// The justification is mandatory and audit-trailed via the revision block.
// This is the escape hatch for dead or stale checks (environment-denied,
// unsatisfiable, or verified through another path).
func (s *PlanStore) checkReplace(args planArgs) (string, error) {
	// Caller holds s.mu (executeArgs dispatches under the store lock).
	stepID := strings.TrimSpace(args.StepID)
	checkID := strings.TrimSpace(args.CheckID)
	justification := normalizePlanText(args.Justification)
	if stepID == "" || checkID == "" {
		return "", fmt.Errorf("plan: check_replace requires step_id and check_id — example: {\"verb\":\"check_replace\",\"step_id\":\"s1\",\"check_id\":\"c1\",\"justification\":\"why\",\"replacement\":{\"id\":\"fresh\",\"description\":\"verify\",\"tool\":\"read_file\",\"arguments\":{\"path\":\"out\"}}}")
	}
	if justification == "" {
		return "", fmt.Errorf("plan: check_replace requires justification (why the old check is dead/stale)")
	}
	if s.plan == nil {
		return "", fmt.Errorf("plan: no plan to revise — create one first")
	}
	idx := indexOfStep(s.plan.Steps, stepID)
	if idx < 0 {
		return "", fmt.Errorf("plan: check_replace: unknown step id %q", stepID)
	}
	step := &s.plan.Steps[idx]
	ci := -1
	for j, c := range step.Checks {
		if c.ID == checkID {
			ci = j
			break
		}
	}
	if ci < 0 {
		return "", fmt.Errorf("plan: check_replace: unknown check id %q on step %q", checkID, stepID)
	}
	working := clonePlanSteps(s.plan.Steps)
	wStep := &working[idx]
	old := wStep.Checks[ci]
	switch {
	case args.Replacement != nil:
		fresh, err := validatePlanChecks([]planCheckArg{*args.Replacement})
		if err != nil {
			return "", fmt.Errorf("plan: check_replace: replacement: %w", err)
		}
		wStep.Checks[ci] = fresh[0]
	case args.EvidenceNote != "":
		note := normalizePlanText(args.EvidenceNote)
		if runes := []rune(note); len(runes) > maxPlanCheckDescChars {
			note = string(runes[:maxPlanCheckDescChars])
		}
		wStep.Checks[ci] = PlanCheck{
			ID:          old.ID,
			Description: old.Description + " (evidence: " + note + ")",
			Tool:        old.Tool,
			Arguments:   old.Arguments,
			Status:      PlanCheckPassed,
		}
	default:
		return "", fmt.Errorf("plan: check_replace requires replacement {id,description,tool,arguments} or evidence_note — example: {\"verb\":\"check_replace\",\"step_id\":\"s1\",\"check_id\":\"c1\",\"justification\":\"why\",\"evidence_note\":\"diff confirmed expected output\"}")
	}
	wStep.Checks[ci].CallID = ""
	candidate := PlanState{Version: s.nextVersion(), Steps: working, Revision: &PlanRevision{
		Reason:  "check " + stepID + "/" + checkID + " replaced: " + justification,
		Summary: []string{"replaced " + stepID + "/" + checkID},
	}}
	if !checkedPlanFits(candidate, s.maxRenderChars) {
		return "", fmt.Errorf("plan: checked plan exceeds max_render_chars (%d)", s.maxRenderChars)
	}
	s.noteStatusTransitionsLocked(s.plan.Steps, working)
	s.plan = &candidate
	// executeArgs fires the single notify for every effective mutation;
	// notifying here would double-fire plan_updated. Mark the revision so
	// downstream change events carry Revised (same contract as revise).
	s.revisionNotify = true
	return s.renderLocked(), nil
}

func (s *PlanStore) complete(stepID string) (string, error) {
	id := strings.TrimSpace(stepID)
	idx := -1
	if s.plan != nil {
		idx = indexOfStep(s.plan.Steps, id)
	}
	if idx < 0 {
		return "", fmt.Errorf("plan: complete: unknown step id %q", stepID)
	}
	if s.plan.Steps[idx].Status == StepDone {
		return s.renderLocked(), nil // idempotent no-op
	}
	working := clonePlanSteps(s.plan.Steps)
	if !allPlanChecksPassed(working[idx]) {
		return "", fmt.Errorf("plan: complete: step %q has checks that have not passed — retryable: true; blocking checks and satisfying calls: %s", working[idx].ID, blockingChecksDetail(working[idx]))
	}
	working[idx].Status = StepDone
	candidate := PlanState{Version: s.nextVersion(), Steps: working, Revision: cloneRevision(s.plan.Revision)}
	if !checkedPlanFits(candidate, s.maxRenderChars) {
		return "", fmt.Errorf("plan: checked plan exceeds max_render_chars (%d) or uses reserved checks delimiter in title/note", s.maxRenderChars)
	}
	s.noteStatusTransitionsLocked(s.plan.Steps, working)
	s.plan = &candidate
	return s.renderLocked(), nil
}

func (s *PlanStore) get() (string, error) {
	if s.plan == nil {
		return "No active plan.", nil
	}
	return s.renderLocked(), nil
}

func indexOfStep(steps []PlanStep, id string) int {
	for i := range steps {
		if steps[i].ID == id {
			return i
		}
	}
	return -1
}

// noteStatusTransitionsLocked updates the blocked-step streak. Consecutive
// transitions TO blocked increment the streak; create is handled separately;
// any transition TO done or in_progress resets it. After 3 blocked
// transitions the streak fires once and resets. Caller holds s.mu.
func (s *PlanStore) noteStatusTransitionsLocked(oldSteps, newSteps []PlanStep) {
	oldByID := make(map[string]StepStatus, len(oldSteps))
	for _, st := range oldSteps {
		oldByID[st.ID] = st.Status
	}
	reset := false
	blockedN := 0
	for _, st := range newSteps {
		old, ok := oldByID[st.ID]
		if !ok || old == st.Status {
			continue
		}
		switch st.Status {
		case StepBlocked:
			blockedN++
		case StepDone, StepInProgress:
			reset = true
		}
	}
	if reset {
		s.blockedStreak = 0
		s.lastBlocked = false
		return
	}
	if blockedN == 0 {
		return
	}
	s.blockedStreak += blockedN
	s.lastBlocked = true
	if s.blockedStreak >= 3 {
		s.blockedFired = true
		s.blockedStreak = 0
	}
}

// planHintIDs returns ID-only pointers into the plan for engine hints.
// Titles and notes are never included.
func planHintIDs(state PlanState) (inProgress, nextPending string) {
	for _, st := range state.Steps {
		switch st.Status {
		case StepInProgress:
			if inProgress == "" {
				inProgress = st.ID
			}
		case StepPending:
			if nextPending == "" {
				nextPending = st.ID
			}
		}
	}
	return inProgress, nextPending
}

// formatPlanStallSuffix is the ID-only stall hint. escalate (local_write+
// or last outcome denied/blocked) tells the model to stop retrying that
// class and names the next pending step as next_non_mutating.
func formatPlanStallSuffix(state PlanState, escalate bool, class string) string {
	inProgress, nextPending := planHintIDs(state)
	if !escalate {
		var parts []string
		if inProgress != "" {
			parts = append(parts, "in_progress="+inProgress)
		}
		if nextPending != "" {
			parts = append(parts, "next_pending="+nextPending)
		}
		if len(parts) == 0 {
			return ""
		}
		return "plan: " + strings.Join(parts, ", ")
	}
	if class == "" {
		class = "mutating"
	}
	var b strings.Builder
	b.WriteString("plan:")
	if inProgress != "" {
		b.WriteString(" in_progress=")
		b.WriteString(inProgress)
	}
	b.WriteString(" stop retrying ")
	b.WriteString(class)
	if nextPending != "" {
		b.WriteString("; next_non_mutating=")
		b.WriteString(nextPending)
	}
	return b.String()
}

// formatRemainingPlanSteps lists pending / in_progress / blocked IDs with
// statuses. Done steps are omitted. No titles or notes.
func formatRemainingPlanSteps(state PlanState) string {
	var parts []string
	for _, st := range state.Steps {
		if st.Status == StepDone {
			continue
		}
		parts = append(parts, st.ID+"="+string(st.Status))
	}
	return strings.Join(parts, " ")
}

// formatRemainingPlanStepsDetailed is the side-call variant: it appends the
// normalized step title ("s1=pending - Ship the parser"). Titles are
// model-authored, already on the main transcript, and stored normalized
// (newlines/em dashes flattened at create), so the grammar stays one step
// per token. The summarizer needs them — its payload may be the only
// context where the plan survives after a trim.
func formatRemainingPlanStepsDetailed(state PlanState) string {
	var parts []string
	for _, st := range state.Steps {
		if st.Status == StepDone {
			continue
		}
		if st.Title != "" {
			// ';' is the list separator on this surface — strip it from titles
			// (nothing machine-parses the list, but unambiguous tokens keep
			// the summarizer from seeing phantom steps). Titles are normalized
			// at create time, but re-normalize at render: older/tampered stores
			// can hold raw newlines that would inject phantom lines.
			title := normalizePlanText(strings.ReplaceAll(st.Title, ";", ","))
			parts = append(parts, st.ID+"="+string(st.Status)+" - "+title)
		} else {
			parts = append(parts, st.ID+"="+string(st.Status))
		}
	}
	return strings.Join(parts, "; ")
}

// normalizePlanText flattens text so the rendered line grammar stays
// unambiguous: newlines become spaces (one step = one line) and em dashes
// become hyphens (the renderer reserves " — " as the title/note separator).
// Applied at validation time (so stored state is clean) and again at render
// time (idempotent, so even directly-constructed states render parseably).
func normalizePlanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "—", "-")
	return strings.TrimSpace(s)
}

// ── Rendering ─────────────────────────────────────────────────────────

// planMsgPrefix marks the protected plan system message so trimming can
// recognize, preserve, and update it (mirrors digestMsgPrefix).
const planMsgPrefix = "[Current plan:"
const planCheckedHeaderMarker = ", checks"

// isPlanMessage reports whether m is the protected plan message.
func isPlanMessage(m session.Message) bool {
	return m.Role == "system" && strings.HasPrefix(m.Content, planMsgPrefix)
}

// planOverflowMarker announces dropped done steps after header truncation.
const planOverflowMarker = "[+%d done steps omitted]"

// planTruncatedMarker terminates a render that could not fit even after all
// done steps were dropped. The resume parser rejects truncated plans
// (fail-closed) rather than approximating their content.
const planTruncatedMarker = "[plan truncated: exceeded max_render_chars]"

// renderPlan renders the plan deterministically: a header line followed by
// one line per step (`id [status] title — note`). When every step is done
// the render collapses to the single-line form. Overflow beyond maxChars
// drops the oldest done steps first (with an explicit marker); if the
// remainder still does not fit, the tail is hard-truncated.
func renderPlan(p PlanState, maxChars int) string {
	header := planHeaderLine(p)
	if allStepsDone(p) && p.Revision == nil {
		return header
	}
	lines := make([]string, 0, len(p.Steps))
	for _, st := range p.Steps {
		lines = append(lines, planStepLine(st))
	}
	build := func(omit map[int]bool, omitted int) string {
		parts := []string{header}
		if p.Revision != nil {
			if b, err := json.Marshal(p.Revision); err == nil {
				parts = append(parts, "[Plan revision: "+string(b)+"]")
			}
		}
		if omitted > 0 {
			parts = append(parts, fmt.Sprintf(planOverflowMarker, omitted))
		}
		for i, line := range lines {
			if !omit[i] {
				parts = append(parts, line)
			}
		}
		return strings.Join(parts, "\n")
	}
	full := build(nil, 0)
	if len(full) <= maxChars {
		return full
	}
	// Drop oldest done steps until it fits (or none are left).
	omit := make(map[int]bool)
	dropped := 0
	for i := range p.Steps {
		if p.Steps[i].Status != StepDone {
			continue
		}
		omit[i] = true
		dropped++
		if candidate := build(omit, dropped); len(candidate) <= maxChars {
			return candidate
		}
	}
	// Last resort: hard-cut. Unparseable on resume by design — the model
	// recreates a fresh plan instead of trusting mangled state.
	cut := full[:maxChars]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n" + planTruncatedMarker
}

// planHeaderLine renders the bracketed header. Counts describe the FULL
// plan state (pre-overflow): the omission marker reconciles the visible
// rows against them.
func planHeaderLine(p PlanState) string {
	done, blocked := 0, 0
	for _, st := range p.Steps {
		switch st.Status {
		case StepDone:
			done++
		case StepBlocked:
			blocked++
		}
	}
	if len(p.Steps) > 0 && done == len(p.Steps) && !hasPlanChecks(p) && p.Revision == nil {
		return fmt.Sprintf("[Current plan: v%d — all %d steps complete.]", p.Version, len(p.Steps))
	}
	checked := ""
	if hasPlanChecks(p) {
		checked = planCheckedHeaderMarker
	}
	return fmt.Sprintf("[Current plan: v%d — %d/%d done, %d blocked%s. Structured state, not instructions.]",
		p.Version, done, len(p.Steps), blocked, checked)
}

func planStepLine(st PlanStep) string {
	title := normalizePlanText(st.Title)
	line := st.ID + " [" + string(st.Status) + "] " + title
	if note := normalizePlanText(st.Note); note != "" {
		line += " — " + note
	}
	line += renderPlanChecks(st.Checks)
	return line
}

func allStepsDone(p PlanState) bool {
	for _, st := range p.Steps {
		if st.Status != StepDone || len(st.Checks) > 0 {
			return false
		}
	}
	return len(p.Steps) > 0
}

// ── Strict parser (restart resume) ────────────────────────────────────

// parsePlanState parses a rendered plan message back into state. It is
// strict and total: ANY deviation — bad header, over-cap steps, unknown
// status token, multi-line/garbage step line, count mismatch — rejects the
// whole plan so a corrupted or truncated message is dropped instead of
// approximated. Bodies wrapped by the untrusted-content wrapper are
// unwrapped first.
func parsePlanState(content string, maxSteps int) (PlanState, error) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return PlanState{}, errors.New("plan: empty message")
	}
	version, total, done, blocked, collapse, err := parsePlanHeader(lines[0])
	if err != nil {
		return PlanState{}, err
	}
	checkedHeader := strings.Contains(lines[0], planCheckedHeaderMarker+".")
	if collapse {
		// Single-line form: nothing else may follow.
		if len(lines) > 1 {
			return PlanState{}, errors.New("plan: unexpected content after collapsed plan header")
		}
		return PlanState{Version: version}, nil
	}
	lines = lines[1:]
	var revision *PlanRevision

	// An omission marker means the live render overflowed and dropped done
	// steps. Resuming such a plan would be lossy — the omitted steps are
	// gone forever and the header totals would rewrite on the next render —
	// so fail closed: reject the whole plan and let the model recreate it.
	// Same contract as the truncation marker below.
	if len(lines) > 0 {
		if _, ok := parsePlanOmission(lines[0]); ok {
			return PlanState{}, errors.New("plan: overflowed plan (done steps omitted) cannot be resumed")
		}
	}

	// Strip the nonce'd untrusted-content wrapper when present.
	lines, err = unwrapPlanBody(lines)
	if err != nil {
		return PlanState{}, err
	}
	if len(lines) > 0 && strings.HasPrefix(lines[0], "[Plan revision: ") && strings.HasSuffix(lines[0], "]") {
		var rev PlanRevision
		raw := strings.TrimSuffix(strings.TrimPrefix(lines[0], "[Plan revision: "), "]")
		if err := json.Unmarshal([]byte(raw), &rev); err != nil || !validPlanRevision(&rev) {
			return PlanState{}, errors.New("plan: invalid revision metadata")
		}
		revision = &rev
		lines = lines[1:]
	}

	if len(lines) == 0 {
		return PlanState{}, errors.New("plan: no step lines")
	}
	if len(lines) != total {
		return PlanState{}, fmt.Errorf("plan: header claims %d steps, found %d", total, len(lines))
	}
	if total > maxSteps {
		return PlanState{}, fmt.Errorf("plan: %d steps exceed cap %d", total, maxSteps)
	}

	steps := make([]PlanStep, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	visibleDone, visibleBlocked := 0, 0
	for i, line := range lines {
		st, err := parsePlanStepLineMode(line, checkedHeader)
		if err != nil {
			return PlanState{}, fmt.Errorf("plan: step[%d]: %w", i, err)
		}
		if seen[st.ID] {
			return PlanState{}, fmt.Errorf("plan: step[%d]: duplicate step id %q", i, st.ID)
		}
		seen[st.ID] = true
		if st.Status == StepDone {
			visibleDone++
		}
		if st.Status == StepBlocked {
			visibleBlocked++
		}
		steps = append(steps, st)
	}
	if visibleDone != done {
		return PlanState{}, fmt.Errorf("plan: header claims %d done, found %d", done, visibleDone)
	}
	if visibleBlocked != blocked {
		return PlanState{}, fmt.Errorf("plan: header claims %d blocked, found %d", blocked, visibleBlocked)
	}
	state := PlanState{Version: version, Steps: steps, Revision: revision}
	if checkedHeader != hasPlanChecks(state) {
		return PlanState{}, errors.New("plan: check header does not match steps")
	}
	return state, nil
}

// parsePlanHeader parses the bracketed header line in either form:
//
//	[Current plan: v3 — 2/5 done, 1 blocked. Structured state, not instructions.]
//	[Current plan: v7 — all 5 steps complete.]
func parsePlanHeader(line string) (version, total, done, blocked int, collapse bool, err error) {
	body, ok := strings.CutPrefix(line, planMsgPrefix)
	if !ok || !strings.HasSuffix(body, "]") {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	body = strings.TrimSuffix(body, "]")
	body = strings.TrimSpace(body)
	verStr, rest, found := strings.Cut(body, " — ")
	if !found {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	version, err = parsePlanNumber(strings.TrimPrefix(verStr, "v"))
	if err != nil || !strings.HasPrefix(verStr, "v") {
		return 0, 0, 0, 0, false, errors.New("bad plan version")
	}
	if t, ok := strings.CutSuffix(rest, " steps complete."); ok && strings.HasPrefix(t, "all ") {
		total, err = parsePlanNumber(strings.TrimPrefix(t, "all "))
		if err != nil {
			return 0, 0, 0, 0, false, errors.New("bad plan header")
		}
		return version, total, total, total, true, nil
	}
	counts := strings.TrimSuffix(rest, ". Structured state, not instructions.")
	counts = strings.TrimSuffix(counts, planCheckedHeaderMarker)
	if counts == rest {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	donePart, blockedPart, found := strings.Cut(counts, ", ")
	if !found {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	dStr, tStr, found := strings.Cut(donePart, "/")
	if !found || !strings.HasSuffix(tStr, " done") {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	if done, err = parsePlanNumber(dStr); err != nil {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	if total, err = parsePlanNumber(strings.TrimSuffix(tStr, " done")); err != nil {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	bStr, ok := strings.CutSuffix(blockedPart, " blocked")
	if !ok {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	if blocked, err = parsePlanNumber(bStr); err != nil {
		return 0, 0, 0, 0, false, errors.New("bad plan header")
	}
	return version, total, done, blocked, false, nil
}

// parsePlanOmission recognizes the `[+N done steps omitted]` marker line.
// Its presence is a resume-rejection condition (see parsePlanState) — the
// marker is legitimate only in the live in-context render.
func parsePlanOmission(line string) (int, bool) {
	body, ok := strings.CutPrefix(line, "[+")
	if !ok {
		return 0, false
	}
	body, ok = strings.CutSuffix(body, " done steps omitted]")
	if !ok {
		return 0, false
	}
	n, err := parsePlanNumber(body)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// unwrapPlanBody strips the nonce'd untrusted-content wrapper the engine
// applies around the step lines. Both tags must be present and the close
// tag must be the LAST line — anything after it is corruption.
func unwrapPlanBody(lines []string) ([]string, error) {
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "<untrusted_content_") {
		return lines, nil
	}
	open := lines[0]
	tagEnd := strings.Index(open, ">")
	if tagEnd < 0 {
		return nil, errors.New("plan: malformed untrusted wrapper")
	}
	closeIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "</untrusted_content_") {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, errors.New("plan: unterminated untrusted wrapper")
	}
	if closeIdx != len(lines)-1 {
		return nil, errors.New("plan: content after wrapper close tag")
	}
	if closeIdx == 0 {
		return nil, errors.New("plan: empty untrusted wrapper")
	}
	inner := make([]string, 0, closeIdx)
	if first := open[tagEnd+1:]; strings.TrimSpace(first) != "" {
		inner = append(inner, first)
	}
	inner = append(inner, lines[1:closeIdx]...)
	// Drop empty leading/trailing artifacts of the wrapper newlines.
	for len(inner) > 0 && strings.TrimSpace(inner[0]) == "" {
		inner = inner[1:]
	}
	for len(inner) > 0 && strings.TrimSpace(inner[len(inner)-1]) == "" {
		inner = inner[:len(inner)-1]
	}
	if len(inner) == 0 {
		return nil, errors.New("plan: empty untrusted wrapper")
	}
	return inner, nil
}

// parsePlanStepLine parses one `id [status] title — note` line.
func parsePlanStepLine(line string) (PlanStep, error) {
	return parsePlanStepLineMode(line, true)
}

func parsePlanStepLineMode(line string, allowChecks bool) (PlanStep, error) {
	checks := []PlanCheck(nil)
	if allowChecks {
		if idx := strings.Index(line, planCheckRenderMarker); idx >= 0 {
			var err error
			checks, err = parsePlanChecks(line[idx+len(planCheckRenderMarker):])
			if err != nil {
				return PlanStep{}, fmt.Errorf("invalid checks: %w", err)
			}
			line = line[:idx]
		}
	}
	sep := strings.Index(line, " [")
	if sep <= 0 {
		return PlanStep{}, errors.New("malformed step line")
	}
	id := line[:sep]
	if len(id) > maxPlanIDChars || strings.ContainsAny(id, " \t\n\r[]") {
		return PlanStep{}, fmt.Errorf("invalid step id %q", id)
	}
	rest := line[sep+2:]
	bracket := strings.Index(rest, "]")
	if bracket < 0 {
		return PlanStep{}, errors.New("malformed step line")
	}
	status := StepStatus(rest[:bracket])
	if !validStepStatus(status) {
		return PlanStep{}, fmt.Errorf("unknown status token %q", string(status))
	}
	title := rest[bracket+1:]
	if !strings.HasPrefix(title, " ") || len(title) < 2 {
		return PlanStep{}, errors.New("missing title")
	}
	title = title[1:]
	note := ""
	if idx := strings.Index(title, " — "); idx >= 0 {
		note = strings.TrimSpace(title[idx+len(" — "):])
		title = strings.TrimSpace(title[:idx])
	}
	if title == "" {
		return PlanStep{}, errors.New("missing title")
	}
	return PlanStep{ID: id, Title: title, Status: status, Note: note, Checks: checks}, nil
}

// parsePlanNumber parses a non-negative integer of digits only — signs,
// whitespace, and anything non-numeric are rejected.
func parsePlanNumber(s string) (int, error) {
	if s == "" || len(s) > 9 {
		return 0, fmt.Errorf("bad number %q", s)
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad number %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ── Shared extraction (serve / Telegram surfaces) ─────────────────────

// extractPlanStepCap bounds step-count validation for read-only extraction.
// It mirrors the config layer's max_steps ceiling (internal/config
// planningMaxSteps = 50): every persisted render was validated against a cap
// ≤ this value at creation time, so nothing legitimately persisted can
// exceed it. internal/loop must not import internal/config — same local-
// mirror precedent as defaultPlanMaxSteps above.
const extractPlanStepCap = 50

// ExtractPlan parses the newest parseable plan message out of a message
// history. It is the shared read-only surface for `odek serve`'s
// GET /api/sessions/{id}/plan endpoint and the Telegram /plan_status command,
// so the parsing logic stays single-sourced with the engine's resume path:
// recognition requires role system + the "[Current plan:" prefix
// (isPlanMessage), each candidate goes through the same strict total parser
// (parsePlanState), corrupt messages are skipped fail-closed (a stale or
// mangled plan must never render as authoritative), and the newest
// parseable message wins. ok=false when no parseable plan exists.
//
// Unlike syncPlanFromMessages this never mutates the input history and has
// no engine state to seed; it is safe to call on any transcript snapshot.
func ExtractPlan(messages []session.Message) (*PlanState, bool) {
	// Backward scan with early exit: the first parseable plan message from
	// the end IS the newest parseable one — identical outcome to the forward
	// scan in syncPlanFromMessages without walking the whole transcript.
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if !isPlanMessage(m) {
			continue
		}
		state, err := parsePlanState(m.Content, extractPlanStepCap)
		if err != nil {
			continue // corrupt plan messages are dropped, never authoritative
		}
		return &state, true
	}
	return nil, false
}

// ── Tool ──────────────────────────────────────────────────────────────

// PlanTool implements the built-in `plan` tool. It delegates everything to
// the shared PlanStore (the memory-tool pattern: the CLI layer creates one
// store and hands it to both this tool and the engine via SetPlanStore, so
// mutations are visible to the loop without any late-bound plumbing).
// PlanTool wires the shared store and carries the resolved remind flag
// (planning.remind, default OFF) for engine discovery.
type PlanTool struct {
	Store  *PlanStore
	Remind bool
}

// NewPlanTool creates a PlanTool bound to the given store.
func NewPlanTool(store *PlanStore) *PlanTool { return &PlanTool{Store: store} }

func (t *PlanTool) Name() string { return "plan" }

func (t *PlanTool) Description() string {
	return "Maintain your task plan. Create steps before starting multi-step work; " +
		"update statuses as you go (in_progress when you start a step, done only after " +
		"verifying it); mark blocked with a note explaining why. The plan is shown to you " +
		"on every iteration and survives context trimming — trust it over your memory of " +
		"earlier turns. Per-verb shapes: " + planVerbExample("create") + " " + planVerbExample("update") + ". " +
		"Use revise when the approach changes so acceptance checks stay attached (add/edit/move are the " +
		"everyday operations; split/supersede are rarer). For verifiable work, declare optional checks with " +
		"exact tool arguments before running them in a later batch. Complete checked steps only after their " +
		"tools succeed; do not self-certify. Plans without checks remain advisory."
}

func planChecksSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": maxPlanChecks,
		"description": "Optional checks with exact tool arguments. In revise edit, append only; IDs must not duplicate existing checks. Invoke tools separately through normal approval; only actual outcomes can pass checks.",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"},
		}, "required": []string{"id", "description", "tool", "arguments"}},
	}
}

func planStepSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
		"note": map[string]any{"type": "string"}, "checks": planChecksSchema(),
	}, "required": []string{"id", "title"}}
}

func (t *PlanTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verb": map[string]any{
				"enum": []string{"create", "update", "complete", "revise", "check_replace", "get"},
				"description": "Field map by verb — create → steps[]; update → updates[]; complete → step_id; revise → operations[]; check_replace → step_id+check_id+justification+(replacement|evidence_note); get → no fields. " +
					"create: replace the whole plan (may always reset; the superseded checked plan is archived). update: batch status/note changes. complete: shorthand to mark one step done. " +
					"revise: atomically add/edit/move/split/supersede steps while preserving checked requirements. " +
					"check_replace: replace one dead/stale/environment-denied check with a fresh one or mark it satisfied by equivalent evidence. get: return current plan.",
			},
			"steps": map[string]any{
				"type": "array", "items": planStepSchema(),
				"description": "create only: full ordered step list (1..max_steps). New steps start pending. Existing checked requirements must remain identical; unchanged checked steps retain progress. Prefer revise for incremental changes.",
			},
			"updates": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "string"},
						"status": map[string]any{"enum": []string{"pending", "in_progress", "done", "blocked"}},
						"note":   map[string]any{"type": "string"},
					},
					"required": []string{"id"},
				},
				"description": "update only: applied in array order; unknown id or unknown status fails the whole call (atomic).",
			},
			"step_id": map[string]any{
				"type":        "string",
				"description": "complete/check_replace only: the step to mark done (complete) or whose check is replaced (check_replace). Also accepted as a single-step alias on update (see verb description).",
			},
			"check_id":          map[string]any{"type": "string", "maxLength": maxPlanCheckIDChars, "description": "check_replace only: the check being replaced."},
			"justification":     map[string]any{"type": "string", "maxLength": maxPlanCheckDescChars, "description": "check_replace only: mandatory why the check is dead/stale; audit-trailed."},
			"evidence_note":     map[string]any{"type": "string", "maxLength": maxPlanCheckDescChars, "description": "check_replace only: mark the check satisfied by equivalent verification that ran via other tools."},
			"reason": map[string]any{"type": "string", "maxLength": maxRevisionReason, "description": "revise only: bounded reason for the change."},
			"operations": map[string]any{
				"type": "array", "minItems": 1, "maxItems": maxRevisionOps,
				"description": "revise only: ordered atomic operations. Existing checks cannot be removed or changed. New checks require a later tool batch.",
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"kind":            map[string]any{"enum": []string{"add", "edit", "move", "split", "supersede"}},
					"step_id":         map[string]any{"type": "string", "description": "Existing step for edit/move/split/supersede."},
					"after_id":        map[string]any{"type": "string", "description": "For add/move: place after this existing step. Use only one anchor; omit both to append."},
					"before_id":       map[string]any{"type": "string", "description": "For add/move: place before this existing step. Mutually exclusive with after_id."},
					"title":           map[string]any{"type": "string", "description": "edit only: replace title; changed work loses its prior check evidence and reopens if completed."},
					"note":            map[string]any{"type": "string", "description": "edit only: replace note, including an empty string to clear it."},
					"checks":          planChecksSchema(),
					"carry_checks_to": map[string]any{"type": "string", "description": "Optional. When a split/supersede replaces a checked step, either set this to a replacement ID to give it the original checks (pending fresh verification), or omit it and declare fresh checks on the replacements. Rejected when omitted and no replacement declares checks, so a checked requirement is never silently dropped."},
					"steps":           map[string]any{"type": "array", "minItems": 1, "items": planStepSchema(), "description": "New steps for add/split/supersede. Split requires at least two. Replacement IDs must be unique; carried checks are added to the target's declared checks."},
				}, "required": []string{"kind"}},
			},
		},
		"required": []string{"verb"},
	}
}

func (t *PlanTool) Call(argsJSON string) (string, error) {
	if t.Store == nil {
		return "", errors.New("plan: planning is disabled")
	}
	return t.Store.Execute(argsJSON)
}
