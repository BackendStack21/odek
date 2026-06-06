package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
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

	callCount := 0
	llm := &mockLLM{responses: map[string]string{
		"Summarize": "session summary",
	}}
	// Override SimpleCall to count calls.
	countingLLM := &countCallsLLM{inner: llm, count: &callCount}

	cfg := DefaultMemoryConfig()
	cfg.LLMSearch = boolPtr(true) // explicitly on — FormatEpisodeContext must still skip it
	mm := NewMemoryManager(dir, countingLLM, cfg)

	// Seed episodes via session end (which fires 1 LLM call for the summary).
	msgs := []string{"user: hi", "assistant: built the postgres schema", "user: ok", "assistant: done"}
	mm.OnSessionEndWithProvenance("20260605-a", 5, msgs, EpisodeProvenance{})

	before := callCount

	// FormatEpisodeContext must not add any LLM calls.
	_ = mm.FormatEpisodeContext("postgres schema")
	after := callCount

	if after != before {
		t.Errorf("FormatEpisodeContext made %d LLM call(s); want 0 (should use vector index only)",
			after-before)
	}
}

// countCallsLLM wraps mockLLM and counts SimpleCall invocations.
type countCallsLLM struct {
	inner *mockLLM
	count *int
}

func (c *countCallsLLM) SimpleCall(ctx context.Context, system, user string) (string, error) {
	*c.count++
	return c.inner.SimpleCall(ctx, system, user)
}

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
	i1 := sharedEpisodeIndex(dir)
	i2 := sharedEpisodeIndex(abs)
	if i1 != i2 {
		t.Errorf("expected same singleton instance for relative and absolute path")
	}
}
