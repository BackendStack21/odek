package memory

import (
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
	if err != nil {
		t.Fatal(err)
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
