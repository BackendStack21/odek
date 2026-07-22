package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEpisodeStoreWriteAndList(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	sessionID := "20260519-abc123"
	summary := "User prefers dark mode\nProject uses Go 1.22"

	if err := es.Write(sessionID, summary, 5); err != nil {
		t.Fatal(err)
	}

	// Verify file exists
	path := filepath.Join(dir, sessionID+".md")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	// Verify index updated
	idx, err := es.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 {
		t.Fatalf("expected 1 index entry, got %d", len(idx))
	}
	if idx[0].SessionID != sessionID {
		t.Errorf("expected session %s, got %s", sessionID, idx[0].SessionID)
	}
}

func TestEpisodeStoreReadSummary(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	summary := "Extracted facts from conversation"
	es.Write("sess-001", summary, 3)

	got, err := es.Read("sess-001")
	if err != nil {
		t.Fatal(err)
	}
	if got != summary {
		t.Errorf("expected %q, got %q", summary, got)
	}
}

func TestEpisodeStoreReadMissing(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	_, err := es.Read("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing episode")
	}
}

func TestEpisodeStoreIndexOrdering(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	// Write out of order
	es.Write("sess-003", "third", 1)
	time.Sleep(10 * time.Millisecond)
	es.Write("sess-001", "first", 1)
	time.Sleep(10 * time.Millisecond)
	es.Write("sess-002", "second", 1)

	idx, _ := es.ReadIndex()
	if len(idx) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(idx))
	}

	// Should be newest first (sess-002, sess-001, sess-003)
	if idx[0].SessionID != "sess-002" {
		t.Errorf("expected sess-002 first, got %s", idx[0].SessionID)
	}
}

func TestEpisodeStoreIndexPersistence(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	es.Write("sess-001", "first", 3)
	es.Write("sess-002", "second", 5)

	// New store instance reading same dir
	es2 := NewEpisodeStore(dir, nil)
	idx, _ := es2.ReadIndex()
	if len(idx) != 2 {
		t.Fatalf("expected 2 entries after reload, got %d", len(idx))
	}
}

func TestEpisodeStoreSearchMock(t *testing.T) {
	dir := t.TempDir()

	// Mock ranker: always returns episodes containing "auth"
	mockRank := func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		var filtered []EpisodeMeta
		for _, ep := range episodes {
			if strings.Contains(ep.Summary, "auth") {
				filtered = append(filtered, ep)
			}
		}
		return filtered, nil
	}

	es := NewEpisodeStore(dir, mockRank)
	es.Write("sess-001", "fixed auth token validation", 5)
	es.Write("sess-002", "optimized database queries", 3)
	es.Write("sess-003", "refactored auth middleware", 7)

	results, err := es.Search("auth", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 auth-related episodes, got %d", len(results))
	}
}

func TestEpisodeStoreSearchLimit(t *testing.T) {
	dir := t.TempDir()

	mockRank := func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		return episodes, nil // return all
	}

	es := NewEpisodeStore(dir, mockRank)
	for i := 0; i < 5; i++ {
		es.Write(fmt.Sprintf("sess-00%d", i), "some summary", 3)
	}

	results, err := es.Search("test", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(results))
	}
}

func TestEpisodeStoreNoIndex(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	// Should not error on empty store
	idx, err := es.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 0 {
		t.Errorf("expected empty index, got %d", len(idx))
	}
}

func TestEpisodeStoreMinTurns(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	// 2 turns — below threshold of 3
	err := es.WriteIfEnough("sess-001", "something", 2)
	if err != nil {
		t.Fatal(err)
	}
	// Should NOT have written
	if _, err := os.Stat(filepath.Join(dir, "sess-001.md")); !os.IsNotExist(err) {
		t.Error("episode should not have been written for <3 turns")
	}

	// 3 turns — should write
	err = es.WriteIfEnough("sess-002", "something else", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sess-002.md")); err != nil {
		t.Error("episode should have been written for >=3 turns")
	}
}

func TestEpisodeStoreLargeSummaryTruncation(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	// 2000 bytes — above 1024 max
	longSummary := strings.Repeat("fact. ", 500)
	err := es.Write("sess-001", longSummary, 5)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := es.Read("sess-001")
	if len(content) > 1050 {
		t.Errorf("summary should be truncated, got %d bytes", len(content))
	}
	if !strings.HasSuffix(content, "...") {
		t.Error("truncated summary should end with ...")
	}
}

func TestNewLLMRanker_EmptyEpisodes(t *testing.T) {
	llm := &mockLLM{}
	ranker := NewLLMRanker(llm)
	results, err := ranker("query", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty episodes, got %d", len(results))
	}
}

func TestNewLLMRanker_LLMFailure(t *testing.T) {
	// LLM returns empty string (simulates failure)
	llm := &mockLLM{responses: map[string]string{}}
	ranker := NewLLMRanker(llm)

	eps := []EpisodeMeta{
		{SessionID: "sess-001", Summary: "auth bug fix", Turns: 5},
		{SessionID: "sess-002", Summary: "database optimization", Turns: 3},
	}

	results, err := ranker("auth", eps)
	if err != nil {
		t.Fatal(err)
	}
	// Should fall back to recency ordering
	if len(results) != 2 {
		t.Errorf("expected 2 results on LLM failure, got %d", len(results))
	}
}

func TestNewLLMRanker_ParsesRanking(t *testing.T) {
	llm := &mockLLM{responses: map[string]string{
		"Rank these memory": "1,0",
	}}
	ranker := NewLLMRanker(llm)

	eps := []EpisodeMeta{
		{SessionID: "sess-001", Summary: "auth bug fix", Turns: 5},
		{SessionID: "sess-002", Summary: "database optimization", Turns: 3},
		{SessionID: "sess-003", Summary: "frontend styling", Turns: 2},
	}

	results, err := ranker("database", eps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// sess-002 should be first (index 1), then sess-001 (index 0)
	if results[0].SessionID != "sess-002" {
		t.Errorf("expected sess-002 first, got %s", results[0].SessionID)
	}
}

func TestNewLLMRanker_NoneRelevant(t *testing.T) {
	llm := &mockLLM{responses: map[string]string{
		"Rank these memory": "none",
	}}
	ranker := NewLLMRanker(llm)

	eps := []EpisodeMeta{
		{SessionID: "sess-001", Summary: "auth bug fix", Turns: 5},
	}

	results, err := ranker("irrelevant", eps)
	// An explicit "none relevant" is signalled with the sentinel error so
	// callers can distinguish it from a rerank FAILURE (which falls back to
	// the unranked candidates).
	if !errors.Is(err, errNoRelevantEpisodes) {
		t.Fatalf("expected errNoRelevantEpisodes for 'none', got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'none', got %d", len(results))
	}
}

func TestNewLLMRanker_DeduplicatesIndices(t *testing.T) {
	llm := &mockLLM{responses: map[string]string{
		"Rank these memory": "0,0,1",
	}}
	ranker := NewLLMRanker(llm)

	eps := []EpisodeMeta{
		{SessionID: "sess-001", Summary: "first", Turns: 3},
		{SessionID: "sess-002", Summary: "second", Turns: 3},
	}

	results, err := ranker("test", eps)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 deduplicated results, got %d", len(results))
	}
}

func TestEpisodeStore_Write_TruncatesSummary(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, func(q string, eps []EpisodeMeta) ([]EpisodeMeta, error) {
		return eps, nil
	})
	// Generate a summary longer than 1024 bytes
	longSummary := strings.Repeat("This is a very long summary that should be truncated. ", 50)
	err := es.Write("sess-trunc", longSummary, 5)
	if err != nil {
		t.Fatal(err)
	}
	content, err := es.Read("sess-trunc")
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > 1100 {
		t.Errorf("summary too long after truncation: %d bytes", len(content))
	}
	if !strings.HasSuffix(content, "...") {
		t.Errorf("expected truncated suffix '...', got %q", content[len(content)-10:])
	}
}

func TestEpisodeStore_SkipShortSessions(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	// 2 turns < threshold 3
	err := es.WriteIfEnough("sess-short", "too brief", 2)
	if err != nil {
		t.Fatal(err)
	}
	// Verify nothing was written — no file should exist
	path := filepath.Join(dir, "sess-short.md")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("file should not exist for skipped session: %s", path)
	}
}

// ── Write error path tests ───────────────────────────────────────────

func TestEpisodeStore_Write_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)

	// Pre-create a directory at the target path so WriteFile fails
	targetPath := filepath.Join(dir, "sess-001.md")
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := es.Write("sess-001", "summary", 5)
	if err == nil {
		t.Error("expected error when target path is a directory (WriteFile should fail)")
	}
	if !strings.Contains(err.Error(), "write episode") {
		t.Errorf("expected 'write episode' in error, got: %v", err)
	}
}

func TestEpisodeStore_Write_InvalidDirPath(t *testing.T) {
	dir := t.TempDir()
	// Create a file where the episodes dir should be — MkdirAll will fail
	badPath := filepath.Join(dir, "episodes")
	if err := os.WriteFile(badPath, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	es := NewEpisodeStore(badPath, nil)

	err := es.Write("sess-001", "summary", 5)
	if err == nil {
		t.Error("expected error when episode path is a file (MkdirAll should fail)")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected 'mkdir' in error, got: %v", err)
	}
}

// ── Red test: EpisodeStore.Search data race on lastQuery/lastResult ────────

// TestEpisodeStore_SearchConcurrent detects the data race on lastQuery and
// lastResult fields which are read/written without any synchronization.
// Run with: go test -race -run TestEpisodeStore_SearchConcurrent
func TestEpisodeStore_SearchConcurrent(t *testing.T) {
	dir := t.TempDir()
	mockRank := func(query string, episodes []EpisodeMeta) ([]EpisodeMeta, error) {
		// Return all, sorted by some deterministic order
		return episodes, nil
	}
	es := NewEpisodeStore(dir, mockRank)

	// Write some episodes first.
	for i := 0; i < 10; i++ {
		sid := fmt.Sprintf("sess-%03d", i)
		if err := es.Write(sid, fmt.Sprintf("summary %d", i), i+1); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for index to settle.
	time.Sleep(10 * time.Millisecond)

	// Concurrent Search calls — this SHOULD trigger a race on
	// lastQuery/lastResult if they're not synchronized.
	done := make(chan struct{})
	const N = 20
	for i := 0; i < N; i++ {
		go func(n int) {
			query := fmt.Sprintf("search term %d", n%5)
			results, err := es.Search(query, 5)
			if err != nil {
				t.Errorf("Search(%q) failed: %v", query, err)
			}
			_ = results
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < N; i++ {
		<-done
	}
}

// ── Search query cache invalidation ─────────────────────────────

// TestEpisodeSearchQueryCacheInvalidatedOnWrite verifies that a write drops
// the cached Search result — previously a repeated query kept serving the
// pre-write ranking because writeIndex never cleared lastQuery/lastResult.
func TestEpisodeSearchQueryCacheInvalidatedOnWrite(t *testing.T) {
	rankCalls := 0
	rankFn := func(query string, eps []EpisodeMeta) ([]EpisodeMeta, error) {
		rankCalls++
		out := make([]EpisodeMeta, len(eps))
		copy(out, eps)
		return out, nil
	}
	dir := t.TempDir()
	store := NewEpisodeStore(dir, rankFn)

	if err := store.Write("sess-1", "first episode summary", 5); err != nil {
		t.Fatal(err)
	}
	got, err := store.Search("anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 episode, got %d", len(got))
	}

	if err := store.Write("sess-2", "second episode summary", 5); err != nil {
		t.Fatal(err)
	}
	got, err = store.Search("anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("query cache not invalidated after write: got %d episodes, want 2", len(got))
	}
	if rankCalls != 2 {
		t.Errorf("rankFn called %d times, want 2 (cache must be dropped on write)", rankCalls)
	}
}

// TestEpisodeSearchQueryCacheInvalidatedOnPromote verifies that promoting a
// tainted episode drops the cached Search result so the next identical query
// includes the newly-approved episode.
func TestEpisodeSearchQueryCacheInvalidatedOnPromote(t *testing.T) {
	rankFn := func(query string, eps []EpisodeMeta) ([]EpisodeMeta, error) {
		out := make([]EpisodeMeta, len(eps))
		copy(out, eps)
		return out, nil
	}
	dir := t.TempDir()
	store := NewEpisodeStore(dir, rankFn)

	if err := store.Write("sess-1", "trusted episode", 5); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWithProvenance("sess-2", "tainted episode", 5, EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Search("anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("tainted episode must be filtered: got %d episodes, want 1", len(got))
	}

	if err := store.Promote("sess-2"); err != nil {
		t.Fatal(err)
	}
	got, err = store.Search("anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("query cache not invalidated after promote: got %d episodes, want 2", len(got))
	}
}
