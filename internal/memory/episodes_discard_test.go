package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEpisodeStore_Discard verifies the human-gated discard path: a pending
// (untrusted, unapproved) episode is removed from the index and from disk, so
// it disappears from PendingReview and can never be recalled.
func TestEpisodeStore_Discard(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	if err := es.WriteWithProvenance("20260106-x", "touched external content", 3, EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := es.Write("20260107-y", "clean session", 2); err != nil {
		t.Fatal(err)
	}

	if err := es.Discard("20260106-x"); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	pending, err := es.PendingReview()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after discard = %d entries, want 0", len(pending))
	}
	// EpisodePendingReview fails closed for unknown sessions by design —
	idx, err := es.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range idx {
		if ep.SessionID == "20260106-x" {
			t.Error("discarded episode still present in index")
		}
	}

	// Discarding a non-pending episode (approved / trusted / auto-approved)
	// is refused — only quarantine-removal goes through this gate.
	if err := es.WriteWithProvenance("20260108-ok", "approved", 1, EpisodeProvenance{Untrusted: true, UserApproved: true}); err != nil {
		t.Fatal(err)
	}
	if err := es.Write("20260109-clean", "trusted", 1); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteWithProvenance("20260110-auto", "auto", 1, EpisodeProvenance{Untrusted: true, AutoApproved: true}); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"20260108-ok", "20260109-clean", "20260110-auto"} {
		if err := es.Discard(sid); err == nil {
			t.Errorf("Discard of non-pending episode %s should error", sid)
		}
	}

	// The discarded episode's summary file is removed from disk.
	if _, err := os.Stat(filepath.Join(dir, "20260106-x.md")); !os.IsNotExist(err) {
		t.Errorf("discarded episode summary file still on disk (stat err: %v)", err)
	}

	// Discarding an unknown session errors.
	if err := es.Discard("99999999-z"); err == nil {
		t.Error("Discard of unknown session should error")
	}
}

// TestMemoryManager_DiscardEpisode covers the manager wrapper, including the
// disabled-memory guard.
func TestMemoryManager_DiscardEpisode(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryManager(dir, nil, MemoryConfig{Enabled: boolPtr(true)})
	if err := m.episodes.WriteWithProvenance("20260201-web", "researched X", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := m.DiscardEpisode("20260201-web"); err != nil {
		t.Fatalf("DiscardEpisode: %v", err)
	}
	pending, _ := m.PendingReviewEpisodes()
	if len(pending) != 0 {
		t.Fatalf("pending after discard = %d, want 0", len(pending))
	}

	// Disabled memory: the wrapper must error rather than touch the store.
	off := NewMemoryManager(dir, nil, MemoryConfig{Enabled: boolPtr(false)})
	if err := off.DiscardEpisode("20260201-web"); err == nil {
		t.Error("DiscardEpisode on disabled memory should error")
	}
}
