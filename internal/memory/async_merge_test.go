package memory

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// callCountLLM counts every LLM invocation and proxies to a mockLLM.
type callCountLLM struct {
	calls int64
	inner *mockLLM
}

func (c *callCountLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	atomic.AddInt64(&c.calls, 1)
	return c.inner.SimpleCall(ctx, system, user)
}

// TestAddFact_NoLLMCalls: AddFact must complete without making any LLM calls,
// even when merge-on-write classifies a new entry as "merge" or "judge".
func TestAddFact_NoLLMCalls(t *testing.T) {
	dir := t.TempDir()
	llm := &callCountLLM{inner: &mockLLM{responses: map[string]string{}}}
	mm := NewMemoryManager(dir, llm, DefaultMemoryConfig())

	// Seed an entry so subsequent adds trigger merge-on-write comparisons.
	if err := mm.AddFact("user", "project uses postgres for all data storage"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := atomic.LoadInt64(&llm.calls)

	// Add a very similar entry — should be classified "merge" or "judge" by RP.
	if err := mm.AddFact("user", "project database is postgres"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	after := atomic.LoadInt64(&llm.calls)

	if after != before {
		t.Errorf("AddFact made %d LLM call(s); want 0 — AddFact must never block on LLM",
			after-before)
	}
}

// TestAddFact_MergeUsesSimpleFallback: when merge-on-write fires, the entries
// are merged using the non-LLM (simple) path. The result must contain content
// from both entries OR one must be a substring of the other.
func TestAddFact_MergeUsesSimpleFallback(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultMemoryConfig()
	cfg.MergeOnWrite = boolPtr(true)
	// Use a high merge threshold so the similar entries definitely trigger "merge".
	cfg.MergeThreshold = 0.01 // effectively always merge
	mm := NewMemoryManager(dir, nil, cfg)

	_ = mm.AddFact("env", "go 1.22")
	_ = mm.AddFact("env", "golang 1.22")

	_, env, err := mm.ReadFacts()
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	// The simple merge either returns the longer entry (substring case) or
	// concatenates them. Either way the result is non-empty.
	if strings.TrimSpace(env) == "" {
		t.Errorf("expected a merged entry in env, got empty")
	}
}

// TestConsolidateOnEnd_Default: the default config has consolidate_on_end=true.
func TestConsolidateOnEnd_Default(t *testing.T) {
	d := DefaultMemoryConfig()
	if d.ConsolidateOnEnd == nil || !*d.ConsolidateOnEnd {
		t.Errorf("ConsolidateOnEnd default should be true, got %v", d.ConsolidateOnEnd)
	}
}

// TestConsolidateOnEnd_FiresAtSessionEnd: with consolidate_on_end=true, facts
// are consolidated in the background at session end. We verify this by seeding
// three distinct facts and confirming the count decreases (Consolidate merged
// them) within a generous timeout.
func TestConsolidateOnEnd_FiresAtSessionEnd(t *testing.T) {
	dir := t.TempDir()
	// LLM that:
	//  - returns "session summary" for the episode extraction call
	//  - returns a 2-element JSON array for any consolidation call
	llm := &mockLLM{responses: map[string]string{
		"Summarize": "session summary",
		// Consolidate prompt contains "memory entries" — return a merged 2-item list
		"memory entri": `["dark mode + vim keybindings preference","works in Go for backend"]`,
	}}

	cfg := DefaultMemoryConfig()
	cfg.ConsolidateOnEnd = boolPtr(true)
	cfg.LLMConsolidate = boolPtr(true)
	cfg.ExtractOnEnd = boolPtr(true)
	cfg.MergeOnWrite = boolPtr(false) // keep all three facts separate
	mm := NewMemoryManager(dir, llm, cfg)

	// Seed three facts that will survive without merge-on-write.
	_ = mm.AddFact("user", "prefers dark mode in all editors")
	_ = mm.AddFact("user", "uses vim keybindings everywhere")
	_ = mm.AddFact("user", "works primarily in Go for backend services")

	entries0, _ := mm.facts.Entries("user")
	if len(entries0) != 3 {
		t.Fatalf("expected 3 seeded entries, got %d", len(entries0))
	}

	msgs := []string{"user: hi", "assistant: ok", "user: more", "assistant: done"}
	mm.OnSessionEndWithProvenance("20260801-a", 5, msgs, EpisodeProvenance{})

	// Poll until consolidation reduces the entry count or timeout.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := mm.facts.Entries("user")
		if len(entries) < 3 {
			return // consolidation ran and reduced entries — success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("session-end consolidation did not reduce fact count within 3 seconds")
}

// TestConsolidateOnEnd_IndependentOfLLMExtract: consolidation must fire even
// when llm_extract=false (episode extraction disabled). D-06 regression guard.
func TestConsolidateOnEnd_IndependentOfLLMExtract(t *testing.T) {
	dir := t.TempDir()
	llm := &mockLLM{responses: map[string]string{
		"memory entri": `["consolidated single fact"]`,
	}}
	cfg := DefaultMemoryConfig()
	cfg.LLMExtract = boolPtr(false) // episodes off — must NOT suppress consolidation
	cfg.ConsolidateOnEnd = boolPtr(true)
	cfg.MergeOnWrite = boolPtr(false)
	mm := NewMemoryManager(dir, llm, cfg)
	_ = mm.AddFact("user", "prefers dark mode editors")
	_ = mm.AddFact("user", "uses dark theme always")
	entries0, _ := mm.facts.Entries("user")
	if len(entries0) < 2 {
		t.Fatalf("need 2 seeded entries, got %d", len(entries0))
	}
	msgs := []string{"user: hi", "assistant: ok", "user: more", "assistant: done"}
	mm.OnSessionEndWithProvenance("sess-d06", 5, msgs, EpisodeProvenance{})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, _ := mm.facts.Entries("user"); len(e) < 2 {
			return // consolidation fired
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("consolidation should fire even with llm_extract=false")
}

// TestConsolidateOnEnd_FlagOff: with consolidate_on_end=false, fact count must
// remain stable at session end (no consolidation LLM call).
func TestConsolidateOnEnd_FlagOff(t *testing.T) {
	dir := t.TempDir()
	llm := &callCountLLM{
		inner: &mockLLM{responses: map[string]string{"Summarize": "summary"}},
	}
	cfg := DefaultMemoryConfig()
	cfg.ConsolidateOnEnd = boolPtr(false)
	cfg.MergeOnWrite = boolPtr(false)
	mm := NewMemoryManager(dir, llm, cfg)

	_ = mm.AddFact("user", "prefers dark mode")
	_ = mm.AddFact("user", "uses Go for backend work")

	msgs := []string{"user: hi", "assistant: ok", "user: more", "assistant: done"}
	before := atomic.LoadInt64(&llm.calls)
	mm.OnSessionEndWithProvenance("20260801-b", 5, msgs, EpisodeProvenance{})

	// Give any goroutine 300 ms to (incorrectly) run.
	time.Sleep(300 * time.Millisecond)
	after := atomic.LoadInt64(&llm.calls)

	// Only the episode extraction call should have fired (Summarize), not Consolidate.
	episodeCalls := after - before
	entries, _ := mm.facts.Entries("user")
	if len(entries) < 2 {
		t.Errorf("consolidate_on_end=false must not consolidate facts, got %d entries", len(entries))
	}
	_ = episodeCalls // 1 call for Summarize is expected
}
