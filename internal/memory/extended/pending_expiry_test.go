package extended

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ── B4 step 1: pending_review age expiry ────────────────────────────────
//
// Unconfirmed pending-review inferences accumulated until the count cap
// (default 20) pushed them out — and the count cap keeps the NEWEST, so
// stale chains rode in the protected memory head indefinitely. Age expiry
// drops unconfirmed entries older than the configured window. Confirmed
// facts are NOT touched — this is eviction of unconfirmed inferences only.
// A zero/negative age disables expiry (legacy behavior).

func TestApplyDiff_ExpiredPendingPruned(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = 14

	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	fresh := time.Now().UTC().Add(-2 * 24 * time.Hour)
	u.state.PendingReview = []PendingReview{
		{ID: "old", Field: "focus.blocker", Value: "stale blocker", CreatedAt: old},
		{ID: "fresh", Field: "focus.blocker", Value: "current blocker", CreatedAt: fresh},
	}

	if err := u.applyDiff(context.Background(), userStateDiff{}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}

	for _, p := range u.state.PendingReview {
		if p.ID == "old" {
			t.Error("30-day-old unconfirmed pending entry survived the 14-day expiry window")
		}
	}
	found := false
	for _, p := range u.state.PendingReview {
		if p.ID == "fresh" {
			found = true
		}
	}
	if !found {
		t.Error("2-day-old pending entry must survive the 14-day expiry window")
	}
}

func TestApplyDiff_ZeroAgeDisablesExpiry(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = 0 // legacy: never expire

	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	u.state.PendingReview = []PendingReview{
		{ID: "ancient", Field: "focus.blocker", Value: "year-old inference", CreatedAt: old},
	}

	if err := u.applyDiff(context.Background(), userStateDiff{}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}
	if len(u.state.PendingReview) != 1 {
		t.Errorf("zero age must disable expiry (legacy behavior), pending = %d entries", len(u.state.PendingReview))
	}
}

func TestApplyDiff_NewPendingNotExpiredBySameRun(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = 14

	// A diff adding a fresh entry must not have it pruned in the same call
	// (CreatedAt is stamped at apply time = now).
	if err := u.applyDiff(context.Background(), userStateDiff{
		Pending: []PendingReview{{Field: "style.tone", Value: "concise"}},
	}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}
	if len(u.state.PendingReview) != 1 {
		t.Errorf("freshly inferred pending entry must survive its own applyDiff, pending = %d", len(u.state.PendingReview))
	}
}

func TestSummary_ExpiredEntriesShrinkPendingBlock(t *testing.T) {
	u := NewUserModel()
	u.cfg.UserStatePendingMaxAgeDays = 14

	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	u.state.PendingReview = []PendingReview{
		{ID: "a", Field: "focus.blocker", Value: "stale-a", CreatedAt: old},
		{ID: "b", Field: "focus.task", Value: "stale-b", CreatedAt: old},
	}
	if err := u.applyDiff(context.Background(), userStateDiff{}); err != nil {
		t.Fatalf("applyDiff: %v", err)
	}

	summary := u.Summary()
	if strings.Contains(summary, "Pending review (2)") {
		t.Errorf("summary still advertises 2 pending entries after expiry:\n%.300s", summary)
	}
}
