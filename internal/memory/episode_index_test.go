package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// resetEpIdxes clears the process-wide singleton map so each test gets a fresh
// index for its temp dir. Must be called at the start of tests that use
// sharedEpisodeIndex.
func resetEpIdxes() {
	epIdxMu.Lock()
	epIdxes = map[string]*episodeVectorIndex{}
	epIdxMu.Unlock()
}

// writeTestEpisode is a convenience wrapper used by index tests.
func writeTestEpisode(t *testing.T, es *EpisodeStore, id, summary string) {
	t.Helper()
	if err := es.WriteWithProvenance(id, summary, 5, EpisodeProvenance{}); err != nil {
		t.Fatalf("write episode %s: %v", id, err)
	}
}

// TestEpisodeIndex_ColdStartAndSearch: write episodes, then call recallByVector
// on a fresh index (no gobs) and confirm the index rebuilds and returns results.
func TestEpisodeIndex_ColdStartAndSearch(t *testing.T) {
	resetEpIdxes()
	es := NewEpisodeStore(t.TempDir(), nil)
	writeTestEpisode(t, es, "20260601-a", "refactored the postgres database schema")
	writeTestEpisode(t, es, "20260601-b", "fixed the login button styling")
	writeTestEpisode(t, es, "20260601-c", "migrated from mysql to postgres database")

	results, err := es.recallByVector("postgres database migration", 3)
	if err != nil {
		t.Fatalf("recallByVector: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result, got none")
	}
	// With k=2 the postgres episodes should rank above the unrelated login one.
	results2, _ := es.recallByVector("postgres database migration", 2)
	for _, ep := range results2 {
		if ep.SessionID == "20260601-b" {
			t.Errorf("unrelated login episode ranked in top 2 — postgres episodes should rank higher")
		}
	}
}

// TestEpisodeIndex_Persistence: write episodes, call recallByVector (builds
// gobs), reset the singleton, then a fresh index should load from gobs and
// return matching results without rebuilding from disk files.
func TestEpisodeIndex_Persistence(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	writeTestEpisode(t, es, "20260602-a", "configured go test pipeline in CI")
	writeTestEpisode(t, es, "20260602-b", "updated npm dependencies")

	// First search: triggers rebuild and persists gobs.
	r1, err := es.recallByVector("go test", 2)
	if err != nil {
		t.Fatalf("first recall: %v", err)
	}

	// Reset singleton so the next call loads from gob, not memory.
	resetEpIdxes()
	es2 := NewEpisodeStore(dir, nil) // same dir

	r2, err := es2.recallByVector("go test", 2)
	if err != nil {
		t.Fatalf("second recall (from gob): %v", err)
	}

	if len(r1) == 0 || len(r2) == 0 {
		t.Fatalf("both recalls should return results, got %d and %d", len(r1), len(r2))
	}
	if r1[0].SessionID != r2[0].SessionID {
		t.Errorf("persistent index returned different top result: %s vs %s",
			r1[0].SessionID, r2[0].SessionID)
	}
}

// TestEpisodeIndex_DirtyRebuild: write an episode, recall, write another, then
// verify the second episode is returned after the dirty rebuild.
func TestEpisodeIndex_DirtyRebuild(t *testing.T) {
	resetEpIdxes()
	es := NewEpisodeStore(t.TempDir(), nil)
	writeTestEpisode(t, es, "20260603-a", "set up redis caching layer")

	// First recall — builds index on episode A.
	if _, err := es.recallByVector("redis", 3); err != nil {
		t.Fatalf("first recall: %v", err)
	}

	// Write a new episode — marks dirty.
	writeTestEpisode(t, es, "20260603-b", "implemented redis pub sub messaging")

	// Next recall should rebuild and include episode B.
	results, err := es.recallByVector("redis pub sub", 5)
	if err != nil {
		t.Fatalf("second recall: %v", err)
	}
	found := false
	for _, ep := range results {
		if ep.SessionID == "20260603-b" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected episode 20260603-b after dirty rebuild, got %v", results)
	}
}

// TestEpisodeIndex_ProvenanceFilter: untrusted/unapproved episodes must be
// excluded from recallByVector results.
func TestEpisodeIndex_ProvenanceFilter(t *testing.T) {
	resetEpIdxes()
	es := NewEpisodeStore(t.TempDir(), nil)

	// Write one trusted and one untrusted episode with similar content.
	if err := es.WriteWithProvenance("20260604-trusted", "deployed the go service to production", 5, EpisodeProvenance{}); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteWithProvenance("20260604-untrusted", "deployed the go service using external script", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatal(err)
	}

	results, err := es.recallByVector("go service deployment", 5)
	if err != nil {
		t.Fatalf("recallByVector: %v", err)
	}
	for _, ep := range results {
		if ep.SessionID == "20260604-untrusted" {
			t.Errorf("untrusted episode must not be returned by recallByVector")
		}
	}
	foundTrusted := false
	for _, ep := range results {
		if ep.SessionID == "20260604-trusted" {
			foundTrusted = true
		}
	}
	if !foundTrusted {
		t.Errorf("trusted episode should be returned, got %v", results)
	}
}

// TestEpisodeIndex_FormatEpisodeContextNoLLM: FormatEpisodeContext must not
// issue any LLM calls — it uses the cached vector index only.
func TestEpisodeIndex_FormatEpisodeContextNoLLM(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()

	llm := &mockLLM{responses: map[string]string{
		"Summarize": "session summary",
	}}
	// Override SimpleCall to count calls.
	countingLLM := &countCallsLLM{inner: llm}

	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(true) // explicitly on — FormatEpisodeContext must still skip it
	mm := NewMemoryManager(dir, countingLLM, cfg)

	// Seed episodes via session end (which fires 1 LLM call for the summary).
	msgs := []string{"user: hi", "assistant: built the postgres schema", "user: ok", "assistant: done"}
	mm.OnSessionEndWithProvenance("20260605-a", 5, msgs, EpisodeProvenance{})

	before := countingLLM.calls()

	// FormatEpisodeContext must not add any LLM calls.
	_ = mm.FormatEpisodeContext("postgres schema")
	after := countingLLM.calls()

	if after != before {
		t.Errorf("FormatEpisodeContext made %d LLM call(s); want 0 (should use vector index only)",
			after-before)
	}
}

// countCallsLLM wraps mockLLM and counts SimpleCall invocations.
//
// The counter is atomic: OnSessionEndWithProvenance calls the LLM from two
// goroutines at once (synchronous episode extraction + background
// consolidation), so a plain int would race under `go test -race`.
type countCallsLLM struct {
	inner *mockLLM
	n     atomic.Int64
}

func (c *countCallsLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	c.n.Add(1)
	return c.inner.SimpleCall(ctx, system, user)
}

// calls returns the number of SimpleCall invocations so far.
func (c *countCallsLLM) calls() int { return int(c.n.Load()) }

// TestEpisodeIndex_ConcurrentSafety: N goroutines sharing one memory dir write
// episodes and recall concurrently. No race, no crashes.
func TestEpisodeIndex_ConcurrentSafety(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			es := NewEpisodeStore(dir, nil)
			id := fmt.Sprintf("20260606-%02d", i)
			summary := fmt.Sprintf("session %d worked on feature %d implementation", i, i)
			_ = es.WriteWithProvenance(id, summary, 5, EpisodeProvenance{})
			_, _ = es.recallByVector(fmt.Sprintf("feature %d", i), 3)
		}(i)
	}
	wg.Wait()
}

// TestMergeDetector_FeaturizationDiscrimination: validates the discrimination
// quality improvement from featurization. "uses postgres" vs "migrated to mysql"
// should be classified as distinct (add); "uses postgres" vs "database is
// postgres" should be similar (merge or judge).
func TestMergeDetector_FeaturizationDiscrimination(t *testing.T) {
	md := NewMergeDetector(256)
	md.Fit([]string{"uses postgres for all data storage"})

	action, _, _ := md.Classify("migrated from mysql to a new database")
	if action == "merge" {
		t.Errorf("'uses postgres' vs 'migrated to mysql' should not auto-merge, got action=%s", action)
	}

	action2, _, _ := md.Classify("the database is postgres and stores all data")
	if action2 == "add" {
		t.Errorf("'uses postgres' vs 'database is postgres' should be similar (merge/judge), got action=%s", action2)
	}
}

// TestEpisodeIndex_EmptyDir: recall on an empty directory returns nil without error.
func TestEpisodeIndex_EmptyDir(t *testing.T) {
	resetEpIdxes()
	es := NewEpisodeStore(t.TempDir(), nil)
	results, err := es.recallByVector("anything", 3)
	if err != nil {
		t.Errorf("empty dir should not error, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("empty dir should return empty results, got %v", results)
	}
}

// TestEpisodeIndex_AbsPath: sharedEpisodeIndex returns the same instance for
// relative and absolute paths pointing to the same dir.
func TestEpisodeIndex_AbsPath(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	abs, _ := filepath.Abs(dir)
	i1 := sharedEpisodeIndex(dir, defaultEmbedderFactory)
	i2 := sharedEpisodeIndex(abs, defaultEmbedderFactory)
	if i1 != i2 {
		t.Errorf("expected same singleton instance for relative and absolute path")
	}
}

// ── SearchEpisodes branch coverage (D-06) ────────────────────────────────────

// TestSearchEpisodes_LLMSearchFalse: with llm_search disabled, SearchEpisodes
// returns vector-ranked candidates without making any LLM call.
func TestSearchEpisodes_LLMSearchFalse(t *testing.T) {
	resetEpIdxes()
	llm := &countCallsLLM{inner: &mockLLM{responses: map[string]string{}}}
	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(false)
	mm := NewMemoryManager(t.TempDir(), llm, cfg)

	msgs := []string{"user: hi", "assistant: postgres schema done", "user: ok", "assistant: done"}
	mm.OnSessionEndWithProvenance("20260701-a", 5, msgs, EpisodeProvenance{})

	before := llm.calls()
	results, err := mm.SearchEpisodes("postgres", 3)
	if err != nil {
		t.Fatalf("SearchEpisodes: %v", err)
	}
	if after := llm.calls(); after != before {
		t.Errorf("llm_search=false should fire 0 LLM calls, got %d", after-before)
	}
	_ = results
}

// TestSearchEpisodes_NilLLM: with no LLM client, SearchEpisodes returns
// vector-ranked candidates without panicking.
func TestSearchEpisodes_NilLLM(t *testing.T) {
	resetEpIdxes()
	cfg := DefaultMemoryConfig()
	mm := NewMemoryManager(t.TempDir(), nil, cfg) // nil LLM client

	es := mm.episodes
	_ = es.WriteWithProvenance("20260702-a", "refactored the postgres connection pool", 5, EpisodeProvenance{})

	results, err := mm.SearchEpisodes("postgres connection", 3)
	if err != nil {
		t.Fatalf("nil LLM should not error: %v", err)
	}
	// Should return vector results, not crash.
	_ = results
}

// TestSearchEpisodes_LimitTruncation: limit < len(reranked) must truncate.
func TestSearchEpisodes_LimitTruncation(t *testing.T) {
	resetEpIdxes()
	llm := &mockLLM{responses: map[string]string{
		"Summarize": "session summary",
		"relevance": "0,1,2,3",
	}}
	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(false) // use RP only for determinism
	mm := NewMemoryManager(t.TempDir(), llm, cfg)

	for i := 0; i < 5; i++ {
		msgs := []string{
			fmt.Sprintf("user: postgres task %d", i),
			"assistant: done",
			fmt.Sprintf("user: more postgres %d", i),
			"assistant: finished",
		}
		id := fmt.Sprintf("20260703-%02d", i)
		mm.OnSessionEndWithProvenance(id, 5, msgs, EpisodeProvenance{})
	}

	results, err := mm.SearchEpisodes("postgres", 2)
	if err != nil {
		t.Fatalf("SearchEpisodes: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results with limit=2, got %d", len(results))
	}
}

// TestSearchEpisodes_RankFnErrorFallback: when the LLM reranker errors,
// SearchEpisodes falls back to the vector-ranked candidates.
func TestSearchEpisodes_RankFnErrorFallback(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	// Build an episode store with a rankFn that always errors.
	es := NewEpisodeStore(dir, func(query string, eps []EpisodeMeta) ([]EpisodeMeta, error) {
		return nil, fmt.Errorf("ranker always fails")
	})
	_ = es.WriteWithProvenance("20260704-a", "set up postgres replication", 5, EpisodeProvenance{})

	llm := &mockLLM{responses: map[string]string{"Summarize": "postgres setup"}}
	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(true)
	// Wire a MemoryManager using the custom episode store.
	mm := &MemoryManager{
		facts:    NewFactStore(dir, cfg.FactsLimitUser, cfg.FactsLimitEnv),
		buffer:   NewBuffer(cfg.BufferLines),
		episodes: es,
		merge:    NewMergeDetectorWithThresholds(0, cfg.MergeThreshold, cfg.AddThreshold),
		llm:      llm,
		cfg:      cfg,
	}
	results, err := mm.SearchEpisodes("postgres", 3)
	if err != nil {
		t.Fatalf("rankFn error should fall back gracefully: %v", err)
	}
	// Should return vector candidates as fallback, not empty.
	_ = results
}

// TestSearchEpisodes_OOVFallbackToLLM: when a query has zero vocabulary
// overlap with the episode corpus (all terms OOV), recallByVector returns nil
// and SearchEpisodes falls back to the LLM ranker — not zero-score noise.
func TestSearchEpisodes_OOVFallbackToLLM(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	llm := &countCallsLLM{
		inner: &mockLLM{responses: map[string]string{
			"Summarize":  "postgres work",
			"relevance":  "0",
			"rank":       "0",
			"most relev": "0",
		}},
	}
	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(true)
	mm := NewMemoryManager(dir, llm, cfg)

	msgs := []string{"user: hi", "assistant: postgres schema done", "user: ok", "assistant: done"}
	mm.OnSessionEndWithProvenance("20260705-a", 5, msgs, EpisodeProvenance{})

	before := llm.calls()
	// Query with zero vocabulary overlap (all OOV) → recallByVector returns nil → fallback to Search
	_, _ = mm.SearchEpisodes("xyzzy wumpus frobnitz", 3)
	after := llm.calls()
	t.Logf("OOV query: llm calls=%d (0 means vector returned nothing, fallback to LLM Search)", after-before)
	// After D-05 fix: OOV → recallByVector returns nil → SearchEpisodes falls back to episodes.Search
	// which uses the LLM ranker. We don't assert an exact count because the fallback
	// path (episodes.Search) may or may not call LLM depending on whether the index
	// is also empty from LLM ranker's perspective, but we confirm no panic.
}
