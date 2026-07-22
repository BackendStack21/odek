package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/fsatomic"
)

// Nudge kinds produced by the proactive-nudges engine.
const (
	NudgeKindOpenQuestion = "open_question"
	NudgeKindStaleGoal    = "stale_goal"
	NudgeKindBlocker      = "blocker"
	NudgeKindDrift        = "drift"
)

// Nudge is a single proactive, user-facing suggestion synthesized from
// trusted memory atoms.
type Nudge struct {
	Text          string   `json:"text"`
	Kind          string   `json:"kind"`
	SourceAtomIDs []string `json:"source_atom_ids,omitempty"`
}

const (
	// nudgesFileName is the anti-annoyance state file inside the extended dir.
	nudgesFileName = "nudges.json"
	// defaultProactiveNudges is used when the caller passes maxN <= 0.
	defaultProactiveNudges = 2
	// nudgeCandidateLimit bounds how many open loops feed the synthesis call.
	nudgeCandidateLimit = 10
	// maxNudgeTextChars caps a single nudge's text so a runaway LLM response
	// cannot bloat the caller's prompt.
	maxNudgeTextChars = 300
)

// validNudgeKinds is the defensive whitelist for parsed LLM output.
var validNudgeKinds = map[string]bool{
	NudgeKindOpenQuestion: true,
	NudgeKindStaleGoal:    true,
	NudgeKindBlocker:      true,
	NudgeKindDrift:        true,
}

// nudgeState is the persisted anti-annoyance ledger.
type nudgeState struct {
	LastFiredByKind map[string]time.Time `json:"last_fired_by_kind,omitempty"`
	Day             string               `json:"day,omitempty"`
	SentToday       int                  `json:"sent_today,omitempty"`
}

// nudgePrompt asks the memory LLM to synthesize user-facing nudges in one
// call. Episode themes are deliberately not an input: episodes live in the
// parent memory package, and importing it here would create a cycle.
const nudgePrompt = `You are a proactive-assistant nudge generator. Based on the user's open loops (unanswered questions, stated goals and intentions), stale goals, and current focus, suggest up to %d short, helpful nudges.

Open loops (JSON, newest first):
%s

Stale goals (no activity in %d+ days, JSON):
%s

Current focus (JSON):
%s

Each nudge is an object with:
- "text": one concise, friendly sentence addressed to the user.
- "kind": one of "open_question", "stale_goal", "blocker", "drift".
- "source_atom_ids": the "id" values of the atoms this nudge is based on.

Rules:
- Use "blocker" when the current focus names a blocker, "drift" when current work diverges from a stated goal, "stale_goal" for goals with no recent activity, "open_question" for unanswered questions.
- Only nudge about things genuinely present in the input; never invent tasks.
- If nothing is worth nudging about, return [].

Return ONLY a JSON array of at most %d nudges.`

// ProactiveNudges computes up to maxN nudges as a preview: it performs no
// anti-annoyance checks and records nothing. All failures degrade to an
// empty result with a nil error — proactive features must never break the
// caller.
func (em *ExtendedMemory) ProactiveNudges(ctx context.Context, maxN int) ([]Nudge, error) {
	return em.computeNudges(ctx, maxN)
}

// TakeNudges computes up to maxN nudges and delivers the ones allowed by the
// anti-annoyance caps: the proactive_nudges_enabled master switch (opt-in,
// default off), nudge_max_per_day, and the per-kind nudge_cooldown_hours.
// Delivered nudges are recorded in nudges.json. All failures degrade to an
// empty result with a nil error.
func (em *ExtendedMemory) TakeNudges(ctx context.Context, maxN int) ([]Nudge, error) {
	if em == nil || !em.Enabled() {
		return nil, nil
	}
	if em.cfg.ProactiveNudgesEnabled == nil || !*em.cfg.ProactiveNudgesEnabled {
		return nil, nil
	}

	em.nudgeMu.Lock()
	defer em.nudgeMu.Unlock()

	now := time.Now().UTC()
	state := em.loadNudgeState()
	if state.Day != now.Format("2006-01-02") {
		// Day rollover: the daily budget resets, per-kind cooldowns persist.
		state.Day = now.Format("2006-01-02")
		state.SentToday = 0
	}
	maxPerDay := em.cfg.NudgeMaxPerDay
	if maxPerDay <= 0 {
		maxPerDay = DefaultConfig().NudgeMaxPerDay
	}
	remaining := maxPerDay - state.SentToday
	if remaining <= 0 {
		return nil, nil
	}

	candidates, err := em.computeNudges(ctx, maxN)
	if err != nil || len(candidates) == 0 {
		return nil, nil
	}

	cooldown := time.Duration(em.cfg.NudgeCooldownHours) * time.Hour
	if cooldown <= 0 {
		cooldown = time.Duration(DefaultConfig().NudgeCooldownHours) * time.Hour
	}
	delivered := make([]Nudge, 0, remaining)
	for _, n := range candidates {
		if len(delivered) >= remaining {
			break
		}
		if last, ok := state.LastFiredByKind[n.Kind]; ok && now.Sub(last) < cooldown {
			continue
		}
		delivered = append(delivered, n)
	}
	if len(delivered) == 0 {
		return nil, nil
	}

	if state.LastFiredByKind == nil {
		state.LastFiredByKind = make(map[string]time.Time)
	}
	for _, n := range delivered {
		state.LastFiredByKind[n.Kind] = now
	}
	state.SentToday += len(delivered)
	if err := em.saveNudgeState(state); err != nil {
		log.Printf("extended memory: nudge state save failed: %v", err)
	}
	return delivered, nil
}

// computeNudges gathers open loops, stale goals, and the current focus, then
// synthesizes nudges with a single LLM call. It returns nil on any failure.
func (em *ExtendedMemory) computeNudges(ctx context.Context, maxN int) ([]Nudge, error) {
	if em == nil || !em.Enabled() || em.llm == nil {
		return nil, nil
	}
	if maxN <= 0 {
		maxN = defaultProactiveNudges
	}

	openLoops, err := em.OpenLoops(ctx, nudgeCandidateLimit)
	if err != nil {
		log.Printf("extended memory: nudge open-loops failed: %v", err)
		return nil, nil
	}
	stale := em.staleGoals()
	var focus FocusState
	if em.userModel != nil {
		focus = em.userModel.State().CurrentFocus
	}
	if len(openLoops) == 0 && len(stale) == 0 && focus == (FocusState{}) {
		return nil, nil
	}

	openJSON, _ := json.Marshal(openLoops)
	staleJSON, _ := json.Marshal(stale)
	focusJSON, _ := json.Marshal(focus)
	prompt := fmt.Sprintf(nudgePrompt, maxN, openJSON, em.staleGoalDays(), staleJSON, focusJSON, maxN)
	resp, err := em.llm.SimpleCall(ctx,
		"You are a proactive-assistant nudge generator. Return only a JSON array of nudges.",
		prompt,
	)
	if err != nil {
		log.Printf("extended memory: nudge synthesis LLM failed: %v", err)
		return nil, nil
	}
	return parseNudges(resp, maxN), nil
}

// parseNudges defensively parses the LLM nudge response: unknown kinds,
// empty texts, and malformed JSON are dropped, and the result is capped at
// maxN with each text capped at maxNudgeTextChars.
func parseNudges(resp string, maxN int) []Nudge {
	resp = strings.TrimSpace(resp)
	if resp == "" || resp == "[]" {
		return nil
	}
	jsonResp, ok := extractJSON(resp)
	if !ok {
		log.Printf("extended memory: nudge parse failed: no JSON in response")
		return nil
	}
	var raw []struct {
		Text          string   `json:"text"`
		Kind          string   `json:"kind"`
		SourceAtomIDs []string `json:"source_atom_ids"`
	}
	if err := json.Unmarshal([]byte(jsonResp), &raw); err != nil {
		log.Printf("extended memory: nudge parse failed: %v", err)
		return nil
	}
	out := make([]Nudge, 0, len(raw))
	for _, r := range raw {
		text := strings.TrimSpace(r.Text)
		if text == "" || !validNudgeKinds[r.Kind] {
			continue
		}
		if len(text) > maxNudgeTextChars {
			text = text[:maxNudgeTextChars]
		}
		out = append(out, Nudge{Text: text, Kind: r.Kind, SourceAtomIDs: r.SourceAtomIDs})
		if len(out) >= maxN {
			break
		}
	}
	return out
}

// staleGoals returns trusted goal/intent atoms whose last activity
// (CreatedAt, refreshed when a goal is re-stated) is older than
// nudge_stale_goal_days.
func (em *ExtendedMemory) staleGoals() []MemoryAtom {
	cutoff := time.Now().UTC().Add(-time.Duration(em.staleGoalDays()) * 24 * time.Hour)
	atoms, err := em.store.List()
	if err != nil {
		log.Printf("extended memory: nudge stale-goal list failed: %v", err)
		return nil
	}
	out := make([]MemoryAtom, 0, nudgeCandidateLimit)
	for _, atom := range atoms {
		if atom.Type != TypeGoal && atom.Type != TypeIntent {
			continue
		}
		if IsTaintedSourceClass(atom.SourceClass) || atom.CreatedAt.After(cutoff) {
			continue
		}
		out = append(out, atom)
		if len(out) >= nudgeCandidateLimit {
			break
		}
	}
	return out
}

// staleGoalDays returns the configured stale-goal threshold in days.
func (em *ExtendedMemory) staleGoalDays() int {
	if em.cfg.NudgeStaleGoalDays > 0 {
		return em.cfg.NudgeStaleGoalDays
	}
	return DefaultConfig().NudgeStaleGoalDays
}

// loadNudgeState reads nudges.json. Missing or corrupt files yield a fresh
// state: the anti-annoyance ledger must never wedge nudge delivery.
func (em *ExtendedMemory) loadNudgeState() nudgeState {
	data, err := os.ReadFile(filepath.Join(em.dir, nudgesFileName))
	if err != nil {
		return nudgeState{}
	}
	var state nudgeState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("extended memory: nudge state parse failed: %v", err)
		return nudgeState{}
	}
	return state
}

// saveNudgeState writes nudges.json atomically with restricted permissions.
func (em *ExtendedMemory) saveNudgeState(state nudgeState) error {
	if err := os.MkdirAll(em.dir, 0700); err != nil {
		return fmt.Errorf("extended memory: nudge state mkdir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("extended memory: nudge state marshal: %w", err)
	}
	if err := fsatomic.WriteFile(filepath.Join(em.dir, nudgesFileName), data, 0600); err != nil {
		return fmt.Errorf("extended memory: nudge state write: %w", err)
	}
	return nil
}
