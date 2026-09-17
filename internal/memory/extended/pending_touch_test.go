package extended

import (
	"context"
	"testing"
	"time"
)

// Regression: a dedup hit on (field, value) skipped the entry entirely, so
// a fact re-inferred every session still aged out at the expiry window —
// continuously reinforced knowledge expired while one-offs survived.
// A dedup hit must REFRESH the stored entry's CreatedAt.
func TestApplyDiff_DedupHitRefreshesCreatedAt(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = intPtr(14)

	old := time.Now().UTC().Add(-13 * 24 * time.Hour) // inside the window, but aging
	u.state.PendingReview = []PendingReview{
		{ID: "p1", Field: "focus.blocker", Value: "CI red", CreatedAt: old},
	}

	time.Sleep(2 * time.Millisecond) // ensure a strictly later timestamp
	if err := u.applyDiff(context.Background(), userStateDiff{
		Pending: []PendingReview{{Field: "focus.blocker", Value: "CI red"}},
	}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}

	if len(u.state.PendingReview) != 1 {
		t.Fatalf("pending = %d, want 1 (dedup must not duplicate)", len(u.state.PendingReview))
	}
	if !u.state.PendingReview[0].CreatedAt.After(old) {
		t.Fatalf("dedup hit did not refresh CreatedAt: %v <= %v", u.state.PendingReview[0].CreatedAt, old)
	}
}

// Regression: entries with a ZERO CreatedAt were exempt from age expiry
// forever — legacy or hand-edited stores accumulated entries no expiry
// path could remove. Zero timestamps must be treated as expired (stale).
func TestApplyDiff_ZeroCreatedAtExpired(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = intPtr(14)

	u.state.PendingReview = []PendingReview{
		{ID: "legacy", Field: "focus.blocker", Value: "ancient hand-edited entry"},
	}

	if err := u.applyDiff(context.Background(), userStateDiff{}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}
	if len(u.state.PendingReview) != 0 {
		t.Fatalf("zero-CreatedAt entry survived expiry, pending = %d", len(u.state.PendingReview))
	}
}
