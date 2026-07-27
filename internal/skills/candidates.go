package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ── Recurrence Candidates ─────────────────────────────────────────────
//
// The auto-save pipeline should not turn a one-off session into a skill.
// CandidateStore persists how often each suggestion fingerprint has been
// seen across sessions (<userDir>/.candidates.json); a suggestion is only
// eligible for saving once its count reaches
// AutoSaveConfig.MinOccurrences. Suggestion names are deterministic per
// pattern ("corrected-git", "procedure-docker", ...), so heuristic+name is
// a stable fingerprint even though body text varies between sessions.

// CandidateFileName is the store file inside the skills user dir.
const CandidateFileName = ".candidates.json"

// candidateMaxAge bounds the store: candidates not seen again within this
// window are pruned on save, so the file cannot grow unboundedly.
const candidateMaxAge = 30 * 24 * time.Hour

// CandidateEntry records recurrence of one suggestion fingerprint.
type CandidateEntry struct {
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// CandidateStore is the persistent recurrence record for suggestions.
type CandidateStore struct {
	Candidates map[string]CandidateEntry `json:"candidates"`
}

// candidateFingerprint identifies a suggestion across sessions.
func candidateFingerprint(s SkillSuggestion) string {
	return s.Heuristic + "|" + s.Name
}

// LoadCandidates reads the candidate store from disk. A missing or
// corrupt file yields an empty store (same fail-open posture as the skip
// list: losing counts only delays saves, it never destroys skills).
func LoadCandidates(userDir string) *CandidateStore {
	cs := &CandidateStore{Candidates: make(map[string]CandidateEntry)}
	data, err := os.ReadFile(filepath.Join(userDir, CandidateFileName))
	if err != nil {
		return cs
	}
	_ = json.Unmarshal(data, cs)
	if cs.Candidates == nil {
		cs.Candidates = make(map[string]CandidateEntry)
	}
	return cs
}

// Record increments the fingerprint's recurrence count and returns the
// new total.
func (cs *CandidateStore) Record(fp string, now time.Time) int {
	e := cs.Candidates[fp]
	if e.Count == 0 {
		e.FirstSeen = now
	}
	e.Count++
	e.LastSeen = now
	cs.Candidates[fp] = e
	return e.Count
}

// Save persists the store, pruning entries not seen within candidateMaxAge.
func (cs *CandidateStore) Save(userDir string) error {
	now := time.Now().UTC()
	for fp, e := range cs.Candidates {
		if now.Sub(e.LastSeen) > candidateMaxAge {
			delete(cs.Candidates, fp)
		}
	}
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(userDir, CandidateFileName), data, 0644)
}
