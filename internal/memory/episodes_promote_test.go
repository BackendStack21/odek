package memory

import "testing"

// TestEpisode_PromoteMakesRecallable is the escape-hatch test: a tainted
// episode is excluded from recall and listed as pending; after Promote it is
// recallable and no longer pending.
func TestEpisode_PromoteMakesRecallable(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	if err := es.WriteWithProvenance("20260105-web", "researched a library", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Pending before promote; excluded from Search.
	pending, err := es.PendingReview()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].SessionID != "20260105-web" {
		t.Fatalf("PendingReview = %v, want the tainted episode", pending)
	}
	res, _ := es.Search("any", 10)
	for _, ep := range res {
		if ep.SessionID == "20260105-web" {
			t.Fatal("tainted episode should be excluded from Search before promote")
		}
	}

	// Promote it.
	if err := es.Promote("20260105-web"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// Now recallable and no longer pending.
	pending, _ = es.PendingReview()
	if len(pending) != 0 {
		t.Errorf("PendingReview after promote = %v, want empty", pending)
	}
	res, _ = es.Search("any", 10)
	var saw bool
	for _, ep := range res {
		if ep.SessionID == "20260105-web" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("promoted episode should be returned by Search, got %v", res)
	}
}

// TestEpisode_PromotePersists verifies the approval survives a fresh store
// (i.e. it was written back to the on-disk index).
func TestEpisode_PromotePersists(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	if err := es.WriteWithProvenance("20260106-x", "s", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"http_batch"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := es.Promote("20260106-x"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	fresh := NewEpisodeStore(dir, nil)
	idx, err := fresh.ReadIndex()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx) != 1 || !idx[0].Provenance.UserApproved {
		t.Errorf("UserApproved did not persist: %+v", idx)
	}
}

func TestEpisode_PromoteErrors(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	if err := es.WriteWithProvenance("20260107-a", "s", 5,
		EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := es.Promote("does-not-exist"); err == nil {
		t.Error("promoting an unknown episode should error")
	}
	if err := es.Promote("20260107-a"); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if err := es.Promote("20260107-a"); err == nil {
		t.Error("promoting an already-approved episode should error")
	}
}
