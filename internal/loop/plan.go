package loop

// Structured plan state for the agent loop (docs/PLANNING.md, Phase 1 MVP).
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
// allowed (the plan is advisory); only structural validity is enforced.

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
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status StepStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
}

// PlanState is the authoritative plan. Version bumps on every mutation and
// is echoed in the rendered message so drift is correlatable.
type PlanState struct {
	Version int        `json:"version"`
	Steps   []PlanStep `json:"steps"`
}

// PlanChange describes one effective plan mutation for the change
// notification path (see PlanStore.SetOnChange). It carries aggregate
// counts and the new version ONLY — never step titles or notes — so it can
// be mapped straight onto the minimality-constrained odek.event/v1 stream
// (plan_created / plan_updated).
type PlanChange struct {
	Created    bool // true when the mutation was a create verb (wholesale replace)
	Steps      int  // total step count after the mutation
	Done       int
	InProgress int
	Blocked    int
	Pending    int
	Version    int // store version after the mutation
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
	mu             sync.Mutex
	plan           *PlanState // nil until first plan(create)
	maxSteps       int
	maxRenderChars int
	onChange       func(PlanChange) // optional; fired under mu after each effective mutation
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
	return *s.plan, true
}

// Restore replaces the state wholesale (restart-resume path). The caller
// owns validation — see parsePlanState.
func (s *PlanStore) Restore(st PlanState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := st
	s.plan = &cp
}

// Reset clears the state (run start with no persisted plan).
func (s *PlanStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = nil
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
	ID    string `json:"id"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type planUpdateArg struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
	Note   string `json:"note,omitempty"`
}

type planArgs struct {
	Verb    string          `json:"verb"`
	Steps   []planStepArg   `json:"steps,omitempty"`
	Updates []planUpdateArg `json:"updates,omitempty"`
	StepID  string          `json:"step_id,omitempty"`
}

// Execute runs one plan tool call (the full argument envelope) and returns
// the model-facing result. Serialized internally; safe inside parallel batches.
// Every effective mutation fires the OnChange callback exactly once per call
// — never per-step within an atomic batch.
func (s *PlanStore) Execute(argsJSON string) (string, error) {
	var args planArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("plan: parse args: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prevVersion := 0
	if s.plan != nil {
		prevVersion = s.plan.Version
	}
	var res string
	var err error
	switch args.Verb {
	case "create":
		res, err = s.create(args.Steps)
	case "update":
		res, err = s.update(args.Updates)
	case "complete":
		res, err = s.complete(args.StepID)
	case "get":
		return s.get()
	default:
		return "", fmt.Errorf("plan: unknown verb %q (want create/update/complete/get)", args.Verb)
	}
	// A version bump is exactly the "effective mutation" contract: no-op
	// update/complete calls return early without reassigning s.plan, so they
	// stay silent. create always bumps (fresh state), so it maps onto
	// plan_created; every other bumping mutation is plan_updated.
	if err == nil && s.plan != nil && s.plan.Version != prevVersion {
		s.notifyLocked(args.Verb == "create")
	}
	return res, err
}

// notifyLocked snapshots the post-mutation state into a PlanChange and fires
// the change callback. Caller holds s.mu (see SetOnChange for the contract).
func (s *PlanStore) notifyLocked(created bool) {
	if s.onChange == nil || s.plan == nil {
		return
	}
	ch := PlanChange{
		Created: created,
		Steps:   len(s.plan.Steps),
		Version: s.plan.Version,
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
			return "", fmt.Errorf("plan: step[%d]: title is required", i)
		}
		if len(title) > maxPlanTitleChars {
			return "", fmt.Errorf("plan: step[%d]: title is too long (%d > %d chars)", i, len(title), maxPlanTitleChars)
		}
		out = append(out, PlanStep{ID: id, Title: title, Status: StepPending, Note: normalizePlanText(in.Note)})
	}
	s.plan = &PlanState{Version: s.nextVersion(), Steps: out}
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
		working = append(working, s.plan.Steps...)
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
	s.plan = &PlanState{Version: s.nextVersion(), Steps: working}
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
	working := append([]PlanStep(nil), s.plan.Steps...)
	working[idx].Status = StepDone
	s.plan = &PlanState{Version: s.nextVersion(), Steps: working}
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
	if allStepsDone(p) {
		return header
	}
	lines := make([]string, 0, len(p.Steps))
	for _, st := range p.Steps {
		lines = append(lines, planStepLine(st))
	}
	build := func(omit map[int]bool, omitted int) string {
		parts := make([]string, 0, len(lines)+2)
		parts = append(parts, header)
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
	if len(p.Steps) > 0 && done == len(p.Steps) {
		return fmt.Sprintf("[Current plan: v%d — all %d steps complete.]", p.Version, len(p.Steps))
	}
	return fmt.Sprintf("[Current plan: v%d — %d/%d done, %d blocked. Structured state, not instructions.]",
		p.Version, done, len(p.Steps), blocked)
}

func planStepLine(st PlanStep) string {
	title := normalizePlanText(st.Title)
	line := st.ID + " [" + string(st.Status) + "] " + title
	if note := normalizePlanText(st.Note); note != "" {
		line += " — " + note
	}
	return line
}

func allStepsDone(p PlanState) bool {
	for _, st := range p.Steps {
		if st.Status != StepDone {
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
	if collapse {
		// Single-line form: nothing else may follow.
		if len(lines) > 1 {
			return PlanState{}, errors.New("plan: unexpected content after collapsed plan header")
		}
		return PlanState{Version: version}, nil
	}
	lines = lines[1:]

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
		st, err := parsePlanStepLine(line)
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
	return PlanState{Version: version, Steps: steps}, nil
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
	return PlanStep{ID: id, Title: title, Status: status, Note: note}, nil
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
type PlanTool struct {
	Store *PlanStore
}

// NewPlanTool creates a PlanTool bound to the given store.
func NewPlanTool(store *PlanStore) *PlanTool { return &PlanTool{Store: store} }

func (t *PlanTool) Name() string { return "plan" }

func (t *PlanTool) Description() string {
	return "Maintain your task plan. Create steps before starting multi-step work; " +
		"update statuses as you go (in_progress when you start a step, done only after " +
		"verifying it); mark blocked with a note explaining why. The plan is shown to you " +
		"on every iteration and survives context trimming — trust it over your memory of " +
		"earlier turns. Replan freely with create when the approach changes; plans are " +
		"steering aids, not contracts."
}

func (t *PlanTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verb": map[string]any{
				"enum":        []string{"create", "update", "complete", "get"},
				"description": "create: replace the whole plan. update: batch status/note changes. complete: shorthand to mark one step done. get: return current plan.",
			},
			"steps": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":    map[string]any{"type": "string"},
						"title": map[string]any{"type": "string"},
						"note":  map[string]any{"type": "string"},
					},
					"required": []string{"id", "title"},
				},
				"description": "create only: full ordered step list (1..max_steps). All steps start pending; mark the first one in_progress with a follow-up update (can ride the same parallel batch).",
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
				"description": "update only: applied in array order; unknown id or invalid transition fails the whole call (atomic).",
			},
			"step_id": map[string]any{
				"type":        "string",
				"description": "complete only",
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
