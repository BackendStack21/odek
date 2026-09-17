package extended

import (
	"context"
	"testing"
	"time"
)

// Regression: the dedup set was built from the PRE-expiry PendingReview
// list, so a fresh inference matching a stale entry was dedup-skipped and
// THEN the stale entry was age-expired — the inference was lost entirely.
// Expiry must run before the dedup set is built.
func TestApplyDiff_ExpiryRunsBeforeDedup(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = intPtr(14)

	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)
	u.state.PendingReview = []PendingReview{
		{ID: "stale", Field: "focus.blocker", Value: "CI red on main", CreatedAt: stale},
	}

	// The model re-infers the same (field, value) — the pending entry must
	// be re-created fresh, not dedup-skipped against the doomed stale one.
	if err := u.applyDiff(context.Background(), userStateDiff{
		Pending: []PendingReview{{Field: "focus.blocker", Value: "CI red on main"}},
	}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}

	if len(u.state.PendingReview) != 1 {
		t.Fatalf("pending = %d entries, want 1 (fresh re-inference must survive)", len(u.state.PendingReview))
	}
	got := u.state.PendingReview[0]
	if got.ID == "stale" {
		t.Fatal("stale entry survived; fresh inference was lost to pre-expiry dedup")
	}
	if got.CreatedAt.Before(time.Now().UTC().Add(-time.Hour)) {
		t.Errorf("surviving entry CreatedAt = %v, want a fresh timestamp", got.CreatedAt)
	}
}
