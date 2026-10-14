package extended

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRED_ConsolidateInFreshProcessNeverGroups: ConsolidateAtoms embeds the
// live corpus with the index's embedder (embedTexts) without fitting it. With
// the default RandomProjections backend the embedder is only fitted by an
// index rebuild, which a fresh process — `odek memory extended consolidate`
// is always one — never runs before consolidating, so every vector is
// degenerate, no group reaches the threshold, and the command reports
// "0 atom(s) merged" however many exact near-duplicates the store holds.
func TestRED_ConsolidateInFreshProcessNeverGroups(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticDedupThreshold = floatPtr(0) // keep the near-duplicates apart at add time

	seeder := New(dir, nil, cfg)
	for _, text := range []string{
		"User prefers Go for backend services",
		"User prefers Go for backend services and tools",
		"The deploy pipeline publishes container images to the registry",
	} {
		if err := seeder.AddAtom(context.Background(), MemoryAtom{Text: text, SourceClass: SourceUserSaid, Type: TypePreference}); err != nil {
			t.Fatal(err)
		}
	}
	seeder.Close()

	// A new process (the CLI) opens the store and consolidates.
	llm := newMockLLM("User prefers Go for backend services and tools")
	em := New(dir, llm, cfg)
	defer em.Close()
	merged, err := em.ConsolidateAtoms(context.Background())
	if err != nil {
		t.Fatalf("ConsolidateAtoms: %v", err)
	}
	if merged != 1 || llm.calls() != 1 {
		t.Fatalf("fresh-process consolidation merged %d group(s) with %d LLM call(s); want the near-duplicate pair merged (1, 1)", merged, llm.calls())
	}
}

// TestRED_MaintenanceOpenRewritesUserModel: NewForMaintenance is documented
// (and docs/EXTENDED_MEMORY.md promises for the CLI) to change nothing on
// open — only prune evicts. But the user-model Load run by every open performs
// the pending-review hygiene sweep and persists it: a report-only command
// such as `odek memory extended stats` drops pending reviews whose referenced
// atom is not in the live store (an atom an operator just triaged into
// quarantine counts as missing) and collapses duplicates, rewriting
// user_model.json — without the cross-process user-model lock that
// mutatePendingReview takes, so a concurrent daemon's pending-review write can
// be lost.
func TestRED_MaintenanceOpenRewritesUserModel(t *testing.T) {
	dir := t.TempDir()
	model := []byte(`{
  "pending_review": [
    {"id": "p1", "field": "technical.languages", "value": "Go (from atom 0123456789abcdef0123456789abcdef)", "confidence": 0.8, "created_at": "2026-10-01T00:00:00Z"},
    {"id": "p2", "field": "style.verbosity", "value": "terse", "confidence": 0.7, "created_at": "2026-10-01T00:00:00Z"},
    {"id": "p3", "field": "style.verbosity", "value": "terse", "confidence": 0.6, "created_at": "2026-10-02T00:00:00Z"}
  ]
}`)
	path := filepath.Join(dir, userStateFileName)
	if err := os.WriteFile(path, model, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := NewForMaintenance(dir, nil, cfg)
	defer em.Close()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, model) {
		t.Fatalf("a maintenance (report-only) open rewrote user_model.json:\nbefore:\n%s\nafter:\n%s", model, after)
	}
	if got := len(em.userModel.ListPendingReview()); got != 3 {
		t.Fatalf("maintenance open dropped pending reviews: %d left, want 3", got)
	}
}

// TestRED_ConsolidateDefaultThresholdGroupsParaphrases: with the local
// embedder and the default (unset) threshold, near-paraphrases group while
// unrelated atoms stay apart.
func TestRED_ConsolidateDefaultThresholdGroupsParaphrases(t *testing.T) {
	unrelated := []string{
		"The deploy pipeline publishes container images to the registry",
		"Tests run with the race detector on every push",
		"The database migration uses goose and runs at startup",
		"Prefers dark mode in the terminal editor",
		"Weekly planning happens on Monday mornings",
	}
	pairs := []struct {
		a, b string
		want bool
	}{
		{"User prefers Go for backend services", "User prefers Go for backend services and tools", true},
		{"User lives in Berlin, Germany", "The user lives in Berlin", true},
		// Reordered wording scores ~0.76 with the local embedder: below the bar.
		{"User works at Acme Corp as a backend engineer", "User is a backend engineer at Acme Corp", false},
	}
	if defaultConsolidateThreshold(false) != 0.8 || defaultConsolidateThreshold(true) != 0.9 {
		t.Fatalf("default thresholds: local %v remote %v", defaultConsolidateThreshold(false), defaultConsolidateThreshold(true))
	}
	for _, p := range pairs {
		texts := append([]string{p.a, p.b}, unrelated...)
		atoms := make([]MemoryAtom, len(texts))
		for i, x := range texts {
			atoms[i] = MemoryAtom{ID: x, Text: x}
		}
		vi := newAtomVectorIndex(t.TempDir(), nil, nil)
		vecs, remote, err := vi.embedCorpus(context.Background(), texts)
		if err != nil || remote {
			t.Fatalf("embedCorpus: remote=%v err=%v", remote, err)
		}
		groups := groupBySimilarity(atoms, vecs, defaultConsolidateThreshold(remote))
		grouped := len(groups) == 1 && len(groups[0]) == 2
		if p.want && !grouped || !p.want && len(groups) != 0 {
			t.Errorf("pair %q / %q: groups=%v, want grouped=%v", p.a, p.b, groups, p.want)
		}
	}
}

func TestRED_EmbedCorpusHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	vi := newAtomVectorIndex(t.TempDir(), nil, nil)
	if _, _, err := vi.embedCorpus(ctx, []string{"a", "b"}); err == nil {
		t.Fatal("embedCorpus ignored a cancelled context")
	}
}
