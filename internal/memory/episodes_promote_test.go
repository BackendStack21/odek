package memory

import "testing"

// ── Auto-approve episode learnings (opt-in, off by default) ───────────

// TestEpisode_AutoApprovedIsRecallable: an episode stamped AutoApproved is
// recalled by Search and is not listed as pending — same effect as a human
// promote, but recorded as automatic.
func TestEpisode_AutoApprovedIsRecallable(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	if err := es.WriteWithProvenance("20260301-auto", "auto-approved external research", 5,
		EpisodeProvenance{Untrusted: true, AutoApproved: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := es.Search("any", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 1 || res[0].SessionID != "20260301-auto" {
		t.Errorf("auto-approved episode should be recalled, got %v", res)
	}
	pending, _ := es.PendingReview()
	if len(pending) != 0 {
		t.Errorf("auto-approved episode should not be pending, got %v", pending)
	}
}

// TestDefaultMemoryConfig_AutoApproveOff: the secure default is false.
func TestDefaultMemoryConfig_AutoApproveOff(t *testing.T) {
	d := DefaultMemoryConfig()
	if d.AutoApproveEpisodes == nil || *d.AutoApproveEpisodes {
		t.Errorf("AutoApproveEpisodes default should be false, got %v", d.AutoApproveEpisodes)
	}
}

// TestOnSessionEnd_AutoApproveStamping: with the flag on, an untrusted episode
// extracted at session end is stamped AutoApproved (not UserApproved) and is
// recallable; with the flag off (default) it stays pending/excluded.
func TestOnSessionEnd_AutoApproveStamping(t *testing.T) {
	llm := &mockLLM{responses: map[string]string{"Summarize": "researched a library online"}}
	msgs := []string{"user: hi", "assistant: ok", "user: go", "assistant: done"}
	prov := EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}

	// Flag ON → auto-approved + recallable.
	on := DefaultMemoryConfig()
	on.AutoApproveEpisodes = boolPtr(true)
	mOn := NewMemoryManager(t.TempDir(), llm, on)
	mOn.OnSessionEndWithProvenance("20260303-on", 5, msgs, prov)

	idx, err := mOn.episodes.ReadIndex()
	if err != nil || len(idx) != 1 {
		t.Fatalf("expected 1 episode, got %v err=%v", idx, err)
	}
	p := idx[0].Provenance
	if !p.Untrusted || !p.AutoApproved || p.UserApproved {
		t.Errorf("flag-on episode provenance = %+v; want Untrusted+AutoApproved, not UserApproved", p)
	}
	if res, _ := mOn.SearchEpisodes("any", 10); len(res) != 1 {
		t.Errorf("flag-on episode should be recallable, got %v", res)
	}
	if pend, _ := mOn.PendingReviewEpisodes(); len(pend) != 0 {
		t.Errorf("flag-on episode should not be pending, got %v", pend)
	}

	// Flag OFF (default) → stays untrusted, excluded, pending.
	mOff := NewMemoryManager(t.TempDir(), llm, DefaultMemoryConfig())
	mOff.OnSessionEndWithProvenance("20260304-off", 5, msgs, prov)
	if res, _ := mOff.SearchEpisodes("any", 10); len(res) != 0 {
		t.Errorf("flag-off untrusted episode must be excluded from recall, got %v", res)
	}
	if pend, _ := mOff.PendingReviewEpisodes(); len(pend) != 1 {
		t.Errorf("flag-off untrusted episode should be pending, got %v", pend)
	}
}

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

// TestMemoryManager_PromoteAndPending covers the manager-level wrappers
// (PromoteEpisode / PendingReviewEpisodes), including the disabled-memory guard.
func TestMemoryManager_PromoteAndPending(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryManager(dir, nil, MemoryConfig{Enabled: boolPtr(true)})
	if err := m.episodes.WriteWithProvenance("20260201-web", "researched X", 5,
		EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pending, err := m.PendingReviewEpisodes()
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingReviewEpisodes = %v, err=%v; want 1", pending, err)
	}
	if err := m.PromoteEpisode("20260201-web"); err != nil {
		t.Fatalf("PromoteEpisode: %v", err)
	}
	pending, _ = m.PendingReviewEpisodes()
	if len(pending) != 0 {
		t.Errorf("after promote, pending = %v, want empty", pending)
	}

	// Disabled memory: both wrappers must error rather than touch the store.
	off := NewMemoryManager(dir, nil, MemoryConfig{Enabled: boolPtr(false)})
	if err := off.PromoteEpisode("20260201-web"); err == nil {
		t.Error("PromoteEpisode on disabled memory should error")
	}
	if _, err := off.PendingReviewEpisodes(); err == nil {
		t.Error("PendingReviewEpisodes on disabled memory should error")
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
