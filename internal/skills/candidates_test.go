package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/guard"
)

// recurringBody passes the quality gate and carries no scope markers.
const recurringBody = "## Overview\n\nReusable procedure with enough body text to pass the quality gate minimum of 200 characters. Adding more padding here to ensure we cross that threshold comfortably.\n\n## Step-by-Step\n\n1. go build ./...\n\n## Common Pitfalls\n\n- none\n\n## Verification\n\n- exit 0"

// TestAutoSaveSuggestions_RecurrenceGate pins the cross-session behavior:
// the first occurrence is recorded as Pending (nothing saved), and only
// the recurrence in a later session (store reloaded from disk) saves.
func TestAutoSaveSuggestions_RecurrenceGate(t *testing.T) {
	dir := t.TempDir()
	suggestions := []SkillSuggestion{
		{Name: "procedure-go-build", Heuristic: "multi-step", Body: recurringBody, CommandLog: []string{"go build ./..."}},
	}

	cfg := DefaultSkillsConfig() // MinOccurrences: 2
	cfg.AutoSave.MaxPerRun = 5

	// Session 1: recorded, not saved.
	result := AutoSaveSuggestions(suggestions, dir, "", cfg, nil, guard.Config{}, false)
	if len(result.Pending) != 1 || result.Pending[0] != "procedure-go-build" {
		t.Errorf("expected Pending=[procedure-go-build], got %+v", result)
	}
	if len(result.Saved) != 0 {
		t.Errorf("first occurrence must not save, got %v", result.Saved)
	}
	if _, err := os.Stat(filepath.Join(dir, "procedure-go-build")); !os.IsNotExist(err) {
		t.Error("skill directory must not exist after first occurrence")
	}
	if _, err := os.Stat(filepath.Join(dir, CandidateFileName)); err != nil {
		t.Error("candidate store was not persisted")
	}

	// Session 2 (new process semantics: store reloaded from disk): saves.
	result = AutoSaveSuggestions(suggestions, dir, "", cfg, nil, guard.Config{}, false)
	if len(result.Saved) != 1 || result.Saved[0] != "procedure-go-build" {
		t.Errorf("expected save on recurrence, got %+v", result)
	}
}

// TestAutoSaveSuggestions_RecurrenceDisabled confirms MinOccurrences=1
// disables the gate entirely.
func TestAutoSaveSuggestions_RecurrenceDisabled(t *testing.T) {
	dir := t.TempDir()
	suggestions := []SkillSuggestion{
		{Name: "procedure-go-build", Heuristic: "multi-step", Body: recurringBody},
	}
	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MinOccurrences = 1
	result := AutoSaveSuggestions(suggestions, dir, "", cfg, nil, guard.Config{}, false)
	if len(result.Saved) != 1 {
		t.Errorf("expected immediate save with MinOccurrences=1, got %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, CandidateFileName)); !os.IsNotExist(err) {
		t.Error("candidate store must not be written when the gate is disabled")
	}
}

// TestCandidateFingerprint_KeysOnCommandLog pins the fix for generic-name
// collisions: two sessions that both yield "procedure-ls" but ran
// different commands are NOT the same pattern recurring, so the gate must
// keep them pending forever.
func TestCandidateFingerprint_KeysOnCommandLog(t *testing.T) {
	a := SkillSuggestion{Name: "procedure-ls", Heuristic: "multi-step", CommandLog: []string{"ls -d cmd/*", "go test ./...", "git status", "make build"}}
	b := SkillSuggestion{Name: "procedure-ls", Heuristic: "multi-step", CommandLog: []string{"ls -la", "grep -rn foo .", "git log", "git push"}}
	if candidateFingerprint(a) == candidateFingerprint(b) {
		t.Error("same generic name with different command logs must not share a fingerprint")
	}
	if candidateFingerprint(a) != candidateFingerprint(a) {
		t.Error("identical suggestion must have a stable fingerprint")
	}
}

// TestAutoSaveSuggestions_NoFalseRecurrence: the same generic suggestion
// name from two unrelated sessions must not reach MinOccurrences.
func TestAutoSaveSuggestions_NoFalseRecurrence(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultSkillsConfig() // MinOccurrences: 2
	cfg.AutoSave.MaxPerRun = 5

	session1 := []SkillSuggestion{
		{Name: "procedure-ls", Heuristic: "multi-step", Body: recurringBody, CommandLog: []string{"ls -d cmd/*", "go build ./...", "git status", "make test"}},
	}
	session2 := []SkillSuggestion{
		{Name: "procedure-ls", Heuristic: "multi-step", Body: recurringBody, CommandLog: []string{"ls -la /tmp", "go build ./cmd/app", "git diff", "make lint"}},
	}

	if r := AutoSaveSuggestions(session1, dir, "", cfg, nil, guard.Config{}, false); len(r.Pending) != 1 {
		t.Fatalf("session 1 should pend, got %+v", r)
	}
	r := AutoSaveSuggestions(session2, dir, "", cfg, nil, guard.Config{}, false)
	if len(r.Saved) != 0 {
		t.Errorf("unrelated sessions sharing a generic name must not trigger recurrence, saved %v", r.Saved)
	}
	if len(r.Pending) != 1 {
		t.Errorf("session 2 should also pend as a distinct pattern, got %+v", r)
	}
}

// TestCandidateStore_PrunesStaleEntries covers the age-based pruning that
// bounds the store file.
func TestCandidateStore_PrunesStaleEntries(t *testing.T) {
	dir := t.TempDir()
	cs := &CandidateStore{Candidates: map[string]CandidateEntry{
		"old":    {Count: 1, FirstSeen: time.Now().Add(-60 * 24 * time.Hour), LastSeen: time.Now().Add(-60 * 24 * time.Hour)},
		"recent": {Count: 1, FirstSeen: time.Now(), LastSeen: time.Now()},
	}}
	if err := cs.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded := LoadCandidates(dir)
	if _, ok := loaded.Candidates["old"]; ok {
		t.Error("stale candidate should have been pruned")
	}
	if _, ok := loaded.Candidates["recent"]; !ok {
		t.Error("recent candidate should survive pruning")
	}
}

// TestCandidateFingerprint_Deterministic guards the stability assumption
// the recurrence gate relies on: same pattern → same fingerprint.
func TestCandidateFingerprint_Deterministic(t *testing.T) {
	a := SkillSuggestion{Name: "corrected-git", Heuristic: "user-correction", Body: "body v1"}
	b := SkillSuggestion{Name: "corrected-git", Heuristic: "user-correction", Body: "completely different body v2"}
	if candidateFingerprint(a) != candidateFingerprint(b) {
		t.Error("fingerprint must be stable across varying bodies")
	}
	c := SkillSuggestion{Name: "corrected-docker", Heuristic: "user-correction"}
	if candidateFingerprint(a) == candidateFingerprint(c) {
		t.Error("different patterns must not share a fingerprint")
	}
}
