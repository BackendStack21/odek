package extended

import (
	"context"
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
)

func TestStatsNil(t *testing.T) {
	var em *ExtendedMemory
	got := em.Stats()
	if got.LiveAtoms != 0 || got.QuarantinedAtoms != 0 || got.QuarantineReasons != nil ||
		got.IndexVectors != 0 || got.IndexDirty || got.StoreSizeBytes != 0 ||
		got.RecallTimeouts != 0 || got.RecallFailures != 0 {
		t.Errorf("expected zero Stats on nil em, got %+v", got)
	}
}

func TestStatsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(dir, newMockLLM(), cfg)
	defer em.Close()

	s := em.Stats()
	if s.LiveAtoms != 0 || s.QuarantinedAtoms != 0 {
		t.Errorf("expected zero atom counts, got %+v", s)
	}
	if s.IndexVectors != 0 {
		t.Errorf("expected 0 index vectors, got %d", s.IndexVectors)
	}
	if !s.IndexDirty {
		t.Error("expected dirty index before the first build")
	}
	if s.RecallTimeouts != 0 || s.RecallFailures != 0 {
		t.Errorf("expected zero recall counters, got %+v", s)
	}
}

func TestStatsCounts(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticDedupThreshold = floatPtr(0)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	for _, text := range []string{"User prefers Go for backend services", "Project uses Postgres database"} {
		if err := em.AddAtom(context.Background(), MemoryAtom{Text: text, SourceClass: SourceUserSaid}); err != nil {
			t.Fatal(err)
		}
	}
	id1, _ := generateAtomID()
	id2, _ := generateAtomID()
	if err := em.quarantine.StoreWithReason(MemoryAtom{ID: id1, Text: "x", SourceClass: SourceWeb}, "tainted"); err != nil {
		t.Fatal(err)
	}
	if err := em.quarantine.StoreWithReason(MemoryAtom{ID: id2, Text: "y", SourceClass: SourceUserSaid}, "scan_rejected: injection detected"); err != nil {
		t.Fatal(err)
	}

	s := em.Stats()
	if s.LiveAtoms != 2 {
		t.Errorf("expected 2 live atoms, got %d", s.LiveAtoms)
	}
	if s.QuarantinedAtoms != 2 {
		t.Errorf("expected 2 quarantined atoms, got %d", s.QuarantinedAtoms)
	}
	if s.QuarantineReasons["tainted"] != 1 || s.QuarantineReasons["scan_rejected"] != 1 {
		t.Errorf("expected reason counts {tainted:1, scan_rejected:1}, got %v", s.QuarantineReasons)
	}
	// The adds triggered the post-batch index rebuild via associations.
	if s.IndexVectors != 2 {
		t.Errorf("expected 2 index vectors, got %d", s.IndexVectors)
	}
	if s.IndexDirty {
		t.Error("expected clean index after the post-batch rebuild")
	}
	if s.StoreSizeBytes <= 0 {
		t.Errorf("expected positive store size, got %d", s.StoreSizeBytes)
	}
}

// TestStatsRecallCounters verifies that recall-path errors are counted:
// deadline-exceeded as timeouts, anything else as failures.
func TestStatsRecallCounters(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	cfg.SemanticSearchMinScore = 0.01
	cfg.SemanticSearchRerank = boolPtr(false)
	cfg.PredictiveIntents = 1
	cfg.FollowUpAnticipationEnabled = boolPtr(true)
	em := New(dir, newMockLLM(), cfg)
	em.index.newEmb = func() embedding.TextEmbedder { return newMockEmbedder(vectorDim) }
	em.index.emb = newMockEmbedder(vectorDim)
	defer em.Close()

	if err := em.AddAtom(context.Background(), makeSearchableAtom("User prefers Go for backend services")); err != nil {
		t.Fatal(err)
	}

	em.recall.predictor = NewPredictor(&mockLLMWithError{err: context.DeadlineExceeded}, cfg)
	if _, err := em.recall.queryAtomsWithPrediction(context.Background(), "Go", nil, UserState{}); err != nil {
		t.Fatalf("queryAtomsWithPrediction failed: %v", err)
	}
	if got := em.Stats().RecallTimeouts; got != 1 {
		t.Errorf("expected 1 recall timeout, got %d", got)
	}

	em.recall.predictor = NewPredictor(&mockLLMWithError{err: errors.New("boom")}, cfg)
	if _, err := em.recall.queryAtomsWithPrediction(context.Background(), "Go", nil, UserState{}); err != nil {
		t.Fatalf("queryAtomsWithPrediction failed: %v", err)
	}
	s := em.Stats()
	if s.RecallTimeouts != 1 || s.RecallFailures != 1 {
		t.Errorf("expected 1 timeout and 1 failure, got %+v", s)
	}
}
