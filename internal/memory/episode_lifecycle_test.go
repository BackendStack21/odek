package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// countMDFiles returns the number of *.md episode summary files in dir.
func countMDFiles(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(matches)
}

// writeIndexDirect writes index.json + matching .md files straight to disk,
// bypassing WriteWithProvenance so tests can control CreatedAt (e.g. backdate
// for TTL). Returns nothing; fatals on error.
func writeIndexDirect(t *testing.T, dir string, metas []EpisodeMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if err := os.WriteFile(filepath.Join(dir, m.SessionID+".md"), []byte(m.Summary), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.MarshalIndent(metas, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, episodeIndexFile), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// ── Dedup ──────────────────────────────────────────────────────────────

func TestEpisodeDedup_ReplaceNewestWins(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0.92, 0, 0)

	const summary = "refactored the postgres connection pool and added retry logic"
	writeTestEpisode(t, es, "20260101-a", summary)
	time.Sleep(2 * time.Millisecond)
	writeTestEpisode(t, es, "20260102-b", summary) // identical → cosine 1.0 → replace

	idx, err := es.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 {
		t.Fatalf("expected 1 episode after dedup, got %d", len(idx))
	}
	if idx[0].SessionID != "20260102-b" {
		t.Errorf("expected newest to win (20260102-b), kept %q", idx[0].SessionID)
	}
	if n := countMDFiles(t, dir); n != 1 {
		t.Errorf("expected 1 .md file after dedup, got %d", n)
	}
}

func TestEpisodeDedup_BelowThresholdKeepsBoth(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0.92, 0, 0)

	// Disjoint vocabulary → cosine well below 0.92.
	writeTestEpisode(t, es, "20260101-a", "refactored the postgres database schema migration")
	writeTestEpisode(t, es, "20260102-b", "updated frontend button hover animation styling")

	idx, _ := es.ReadIndex()
	if len(idx) != 2 {
		t.Fatalf("expected 2 distinct episodes, got %d", len(idx))
	}
}

func TestEpisodeDedup_Disabled(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 0, 0) // dedup off

	const summary = "identical episode summary content here"
	writeTestEpisode(t, es, "20260101-a", summary)
	writeTestEpisode(t, es, "20260102-b", summary)

	idx, _ := es.ReadIndex()
	if len(idx) != 2 {
		t.Fatalf("dedup disabled should keep both identical episodes, got %d", len(idx))
	}
}

func TestEpisodeDedup_ProvenanceSafety(t *testing.T) {
	const summary = "investigated the auth token refresh flow in detail"

	t.Run("untrusted does not evict trusted", func(t *testing.T) {
		resetEpIdxes()
		dir := t.TempDir()
		es := NewEpisodeStoreWithLifecycle(dir, nil, 0.92, 0, 0)

		if err := es.WriteWithProvenance("20260101-a", summary, 5, EpisodeProvenance{}); err != nil {
			t.Fatal(err) // trusted
		}
		if err := es.WriteWithProvenance("20260102-b", summary, 5, EpisodeProvenance{Untrusted: true}); err != nil {
			t.Fatal(err) // untrusted near-dup
		}

		idx, _ := es.ReadIndex()
		if len(idx) != 2 {
			t.Fatalf("untrusted near-dup must NOT replace trusted; expected 2, got %d", len(idx))
		}
		// The trusted episode must still be present.
		var trustedPresent bool
		for _, m := range idx {
			if m.SessionID == "20260101-a" && !m.Provenance.Untrusted {
				trustedPresent = true
			}
		}
		if !trustedPresent {
			t.Error("trusted episode 20260101-a was lost")
		}
	})

	t.Run("trusted evicts untrusted", func(t *testing.T) {
		resetEpIdxes()
		dir := t.TempDir()
		es := NewEpisodeStoreWithLifecycle(dir, nil, 0.92, 0, 0)

		if err := es.WriteWithProvenance("20260101-a", summary, 5, EpisodeProvenance{Untrusted: true}); err != nil {
			t.Fatal(err) // untrusted
		}
		if err := es.WriteWithProvenance("20260102-b", summary, 5, EpisodeProvenance{}); err != nil {
			t.Fatal(err) // trusted near-dup → may replace (upgrade)
		}

		idx, _ := es.ReadIndex()
		if len(idx) != 1 {
			t.Fatalf("trusted near-dup should replace untrusted; expected 1, got %d", len(idx))
		}
		if idx[0].SessionID != "20260102-b" || idx[0].Provenance.Untrusted {
			t.Errorf("expected trusted 20260102-b to remain, got %+v", idx[0])
		}
	})
}

// ── Eviction ───────────────────────────────────────────────────────────

func TestEpisodeEviction_ByCount(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 3, 0) // dedup off, cap 3

	for i := 0; i < 5; i++ {
		writeTestEpisode(t, es, fmt.Sprintf("20260101-%02d", i),
			fmt.Sprintf("session number %d distinct work item alpha%d", i, i))
		time.Sleep(2 * time.Millisecond) // ensure strictly increasing CreatedAt
	}

	idx, _ := es.ReadIndex()
	if len(idx) != 3 {
		t.Fatalf("expected cap of 3, got %d", len(idx))
	}
	if n := countMDFiles(t, dir); n != 3 {
		t.Errorf("expected 3 .md files after eviction, got %d", n)
	}
	// The 3 newest (02,03,04) must remain; oldest (00,01) evicted.
	for _, m := range idx {
		if m.SessionID == "20260101-00" || m.SessionID == "20260101-01" {
			t.Errorf("oldest episode %q should have been evicted", m.SessionID)
		}
	}
}

func TestEpisodeEviction_ByTTL(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	now := time.Now().UTC()

	// Two fresh, one 10 days old.
	writeIndexDirect(t, dir, []EpisodeMeta{
		{SessionID: "20260101-a", CreatedAt: now.Add(-1 * time.Hour), Summary: "fresh one"},
		{SessionID: "20260101-b", CreatedAt: now.Add(-2 * time.Hour), Summary: "fresh two"},
		{SessionID: "20260101-c", CreatedAt: now.Add(-10 * 24 * time.Hour), Summary: "stale one"},
	})

	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 0, 7) // TTL 7 days
	if err := es.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	idx, _ := es.ReadIndex()
	if len(idx) != 2 {
		t.Fatalf("expected 2 fresh episodes after TTL prune, got %d", len(idx))
	}
	for _, m := range idx {
		if m.SessionID == "20260101-c" {
			t.Error("stale episode 20260101-c should have been evicted by TTL")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "20260101-c.md")); !os.IsNotExist(err) {
		t.Error("stale .md file should have been removed")
	}
}

func TestEpisodeEviction_TTLDisabled(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	now := time.Now().UTC()
	writeIndexDirect(t, dir, []EpisodeMeta{
		{SessionID: "20260101-a", CreatedAt: now.Add(-365 * 24 * time.Hour), Summary: "ancient"},
	})

	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 0, 0) // TTL disabled
	if err := es.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	idx, _ := es.ReadIndex()
	if len(idx) != 1 {
		t.Fatalf("TTL disabled must retain old episodes, got %d", len(idx))
	}
}

// ── Self-overwrite ─────────────────────────────────────────────────────

func TestEpisodeWrite_SelfOverwriteSingleEntry(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 0, 0)

	writeTestEpisode(t, es, "20260101-a", "first version of the summary")
	writeTestEpisode(t, es, "20260101-a", "second version of the summary")

	idx, _ := es.ReadIndex()
	if len(idx) != 1 {
		t.Fatalf("re-writing same sessionID must not duplicate the index entry, got %d", len(idx))
	}
	full, _ := es.Read("20260101-a")
	if full != "second version of the summary" {
		t.Errorf("expected the latest summary on disk, got %q", full)
	}
}

// ── Recall consistency ─────────────────────────────────────────────────

func TestEpisodeLifecycle_EvictedAbsentFromRecall(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0, 2, 0) // cap 2

	writeTestEpisode(t, es, "20260101-00", "postgres database connection pooling work")
	time.Sleep(2 * time.Millisecond)
	writeTestEpisode(t, es, "20260101-01", "redis caching layer latency tuning")
	time.Sleep(2 * time.Millisecond)
	writeTestEpisode(t, es, "20260101-02", "kafka consumer rebalance bugfix") // evicts -00

	got, err := es.recallByVector("postgres database", 5)
	if err != nil {
		t.Fatalf("recallByVector: %v", err)
	}
	for _, m := range got {
		if m.SessionID == "20260101-00" {
			t.Error("evicted episode 20260101-00 must not appear in recall (index should have rebuilt)")
		}
	}
}

// ── Concurrency ────────────────────────────────────────────────────────

func TestEpisodeLifecycle_ConcurrentSafety(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	const maxEp = 5
	es := NewEpisodeStoreWithLifecycle(dir, nil, 0.92, maxEp, 0)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Mix of distinct and overlapping (near-dup) summaries.
			id := fmt.Sprintf("20260101-%02d", i)
			summary := fmt.Sprintf("worked on subsystem %d with shared common boilerplate text", i%4)
			_ = es.WriteWithProvenance(id, summary, 5, EpisodeProvenance{})
			_, _ = es.recallByVector("subsystem common", 3)
		}(i)
	}
	wg.Wait()

	idx, err := es.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(idx) > maxEp {
		t.Errorf("index length %d exceeds cap %d", len(idx), maxEp)
	}
}

// ── Config wiring ──────────────────────────────────────────────────────

func TestMemoryConfig_EpisodeLifecycleDefaults(t *testing.T) {
	d := DefaultMemoryConfig()
	if d.EpisodeDedupThreshold != defaultEpisodeDedupThreshold {
		t.Errorf("EpisodeDedupThreshold default = %v, want %v", d.EpisodeDedupThreshold, defaultEpisodeDedupThreshold)
	}
	if d.MaxEpisodes != defaultMaxEpisodes {
		t.Errorf("MaxEpisodes default = %d, want %d", d.MaxEpisodes, defaultMaxEpisodes)
	}
	if d.EpisodeTTLDays != 0 {
		t.Errorf("EpisodeTTLDays default = %d, want 0 (disabled)", d.EpisodeTTLDays)
	}
}

func TestMemoryConfig_EpisodeLifecycleOverlayWiredToStore(t *testing.T) {
	resetEpIdxes()
	dir := t.TempDir()
	cfg := DefaultMemoryConfig()
	cfg.EpisodeDedupThreshold = 0 // disable dedup to isolate the cap
	cfg.MaxEpisodes = 2
	mm := NewMemoryManager(dir, nil, cfg)

	for i := 0; i < 4; i++ {
		_ = mm.episodes.WriteWithProvenance(fmt.Sprintf("20260101-%02d", i),
			fmt.Sprintf("distinct task %d zeta%d", i, i), 5, EpisodeProvenance{})
		time.Sleep(2 * time.Millisecond)
	}
	idx, _ := mm.episodes.ReadIndex()
	if len(idx) != 2 {
		t.Fatalf("NewMemoryManager should wire MaxEpisodes=2 into the store; got %d", len(idx))
	}
}
