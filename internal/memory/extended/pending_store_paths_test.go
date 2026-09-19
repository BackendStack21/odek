package extended

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPendingNilStoreInMemoryPath covers the checker-less in-memory model
// (NewUserModel / no backing store): confirm, reject, and list operate on
// the in-memory queue and keep working.
func TestPendingNilStoreInMemoryPath(t *testing.T) {
	um := NewUserModel()
	if err := um.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := um.ListPendingReview(); len(got) != 1 {
		t.Fatalf("expected 1 in-memory pending, got %d", len(got))
	}
	id := um.ListPendingReview()[0].ID
	if err := um.ConfirmPendingReview(id); err != nil {
		t.Fatalf("in-memory confirm failed: %v", err)
	}
	if got := um.State().Style.Tone; got != "dry" {
		t.Errorf("confirm not applied in-memory: %q", got)
	}
	if err := um.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.humor", Value: "wry", Confidence: 0.8, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	id = um.ListPendingReview()[0].ID
	if err := um.RejectPendingReview(id); err != nil {
		t.Fatalf("in-memory reject failed: %v", err)
	}
	if got := um.ListPendingReview(); len(got) != 0 {
		t.Fatalf("expected empty queue after reject, got %d", len(got))
	}
	if err := um.RejectPendingReview("nonexistent"); err == nil {
		t.Error("expected not-found error on missing id")
	}
}

// TestPendingListFailOpenOnReadError verifies that a corrupt store file makes
// List fall back to the last known in-memory snapshot instead of returning
// nil (the agent-side queue view is never lost to a transient read error).
func TestPendingListFailOpenOnReadError(t *testing.T) {
	dir := t.TempDir()
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.applyDiff(nil, userStateDiff{Pending: []PendingReview{{
		Field: "style.tone", Value: "dry", Confidence: 0.9, CreatedAt: time.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	snapshot := um.ListPendingReview()
	if len(snapshot) != 1 {
		t.Fatalf("setup: expected 1 pending, got %d", len(snapshot))
	}

	// Corrupt the store.
	if err := os.WriteFile(filepath.Join(dir, "user_model.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	got := um.ListPendingReview()
	if len(got) != 1 || got[0].ID != snapshot[0].ID {
		t.Fatalf("expected fail-open snapshot, got %+v", got)
	}
}

// TestPendingMutateNotFoundOnEmptyDisk pins that a confirm against an empty
// on-disk queue (no entry ever written) still returns the not-found error
// rather than silently succeeding.
func TestPendingMutateNotFoundOnEmptyDisk(t *testing.T) {
	dir := t.TempDir()
	um := NewUserModelWithStore(dir, newMockLLM(), DefaultConfig())
	if err := um.Load(); err != nil {
		t.Fatal(err)
	}
	if err := um.ConfirmPendingReview("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaazz"); err == nil {
		t.Error("expected not-found error on empty disk queue")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
