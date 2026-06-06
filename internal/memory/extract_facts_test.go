package memory

import (
	"strings"
	"testing"
)

// factLLM returns a mockLLM that answers both end-of-session calls: the episode
// "Summarize…" prompt and the fact "DURABLE" prompt.
func factLLM(factJSON string) *mockLLM {
	return &mockLLM{responses: map[string]string{
		"Summarize": "did some work",
		"DURABLE":   factJSON,
	}}
}

var threeTurns = []string{"user: hi", "assistant: ok", "user: go", "assistant: done"}

// Trusted session with the flag on (default) extracts durable facts into the
// user/env fact files, and still writes the episode.
func TestExtractFacts_TrustedAddsFacts(t *testing.T) {
	dir := t.TempDir()
	llm := factLLM(`[{"scope":"user","fact":"User prefers tabs over spaces"},{"scope":"env","fact":"Project is Go and tests run with go test"}]`)
	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())

	mm.OnSessionEndWithProvenance("20260401-ok", 5, threeTurns, EpisodeProvenance{})

	user, env, err := mm.ReadFacts()
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if !strings.Contains(user, "tabs over spaces") {
		t.Errorf("user fact not stored, got %q", user)
	}
	if !strings.Contains(env, "go test") {
		t.Errorf("env fact not stored, got %q", env)
	}
	// Episode still written (refactor didn't break it).
	if res, _ := mm.SearchEpisodes("any", 5); len(res) != 1 {
		t.Errorf("expected the episode to be written, got %v", res)
	}
}

// Untrusted sessions must NOT auto-write durable facts (security gate), even
// though the episode pipeline may still run.
func TestExtractFacts_UntrustedSkips(t *testing.T) {
	dir := t.TempDir()
	llm := factLLM(`[{"scope":"user","fact":"should not be stored"}]`)
	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())

	mm.OnSessionEndWithProvenance("20260402-web", 5, threeTurns,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}})

	user, env, _ := mm.ReadFacts()
	if user != "" || env != "" {
		t.Errorf("untrusted session must not add facts, got user=%q env=%q", user, env)
	}
}

// Flag off → no facts; episodes unaffected.
func TestExtractFacts_FlagOff(t *testing.T) {
	dir := t.TempDir()
	llm := factLLM(`[{"scope":"user","fact":"should not be stored"}]`)
	cfg := DefaultMemoryConfig()
	cfg.ExtractFacts = boolPtr(false)
	mm := NewMemoryManager(dir, llm, cfg)

	mm.OnSessionEndWithProvenance("20260403-off", 5, threeTurns, EpisodeProvenance{})

	user, env, _ := mm.ReadFacts()
	if user != "" || env != "" {
		t.Errorf("flag off must not add facts, got user=%q env=%q", user, env)
	}
	if res, _ := mm.SearchEpisodes("any", 5); len(res) != 1 {
		t.Errorf("episodes should still work with extract_facts off, got %v", res)
	}
}

// Per-session count cap is honored. Merge-on-write disabled so each distinct
// fact is a separate entry and the cap is the only limiter.
func TestExtractFacts_CountCap(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < maxAutoFactsPerSession+2; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"scope":"user","fact":"distinct durable fact number `)
		sb.WriteByte(byte('a' + i))
		sb.WriteString(`"}`)
	}
	sb.WriteString("]")
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(false)
	mm := NewMemoryManager(dir, factLLM(sb.String()), cfg)

	mm.OnSessionEndWithProvenance("20260404-cap", 5, threeTurns, EpisodeProvenance{})

	entries, _ := mm.facts.Entries("user")
	if len(entries) != maxAutoFactsPerSession {
		t.Errorf("count cap not honored: got %d entries, want %d", len(entries), maxAutoFactsPerSession)
	}
}

// Malformed and empty-array LLM output are no-ops (no facts, no panic/error).
func TestExtractFacts_MalformedAndEmpty(t *testing.T) {
	for _, resp := range []string{"not json at all", "[]", ""} {
		dir := t.TempDir()
		mm := NewMemoryManager(dir, factLLM(resp), DefaultMemoryConfig())
		mm.OnSessionEndWithProvenance("20260405-x", 5, threeTurns, EpisodeProvenance{})
		if user, env, _ := mm.ReadFacts(); user != "" || env != "" {
			t.Errorf("resp %q should add no facts, got user=%q env=%q", resp, user, env)
		}
	}
}

// Bad scopes / empty facts are skipped; only valid user/env entries land.
func TestExtractFacts_FiltersInvalidScopes(t *testing.T) {
	dir := t.TempDir()
	llm := factLLM(`[{"scope":"system","fact":"x"},{"scope":"user","fact":""},{"scope":"env","fact":"valid env fact"}]`)
	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())
	mm.OnSessionEndWithProvenance("20260406-f", 5, threeTurns, EpisodeProvenance{})

	user, env, _ := mm.ReadFacts()
	if user != "" {
		t.Errorf("invalid/empty user facts should be skipped, got %q", user)
	}
	if !strings.Contains(env, "valid env fact") {
		t.Errorf("valid env fact should be stored, got %q", env)
	}
}

func TestDefaultMemoryConfig_ExtractFactsOn(t *testing.T) {
	if d := DefaultMemoryConfig(); d.ExtractFacts == nil || !*d.ExtractFacts {
		t.Errorf("ExtractFacts default should be true, got %v", d.ExtractFacts)
	}
}
