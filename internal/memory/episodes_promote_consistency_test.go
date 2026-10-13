package memory

import (
	"strings"
	"testing"
)

// TestRED_PromoteMirrorsDiscardOnNonPendingEpisodes: promote and discard are
// the two sides of one review gate and must judge an episode the same way.
// An unknown id is "not found" (not a raw file error leaking the path), a
// trusted episode is "not pending review" (promote reported success and
// printed it), and an already approved one is "already approved".
func TestRED_PromoteMirrorsDiscardOnNonPendingEpisodes(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	if err := es.WriteWithProvenance("20260101-trusted", "a trusted session", 5, EpisodeProvenance{}); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteWithProvenance("20260101-pending", "an untrusted session", 5, EpisodeProvenance{Untrusted: true, Sources: []string{"browser"}}); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteWithProvenance("20260101-auto", "an auto-approved session", 5, EpisodeProvenance{Untrusted: true, AutoApproved: true}); err != nil {
		t.Fatal(err)
	}

	_, err := es.PromoteIfHash("20260101-missing", "")
	if err == nil || !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "/") {
		t.Fatalf("promote unknown id: %v; want a plain not-found error", err)
	}
	_, err = es.PromoteIfHash("20260101-trusted", "")
	if err == nil || !strings.Contains(err.Error(), "not pending review") {
		t.Fatalf("promote trusted episode: %v; want not-pending error", err)
	}
	if text, err := es.PromoteIfHash("20260101-pending", ""); err != nil || text != "an untrusted session" {
		t.Fatalf("promote pending: text=%q err=%v", text, err)
	}
	_, err = es.PromoteIfHash("20260101-pending", "")
	if err == nil || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("second promote: %v", err)
	}
	// Auto-approved episodes are recallable already; a human promote makes
	// that durable (survives turning auto_approve_episodes off).
	if _, err := es.PromoteIfHash("20260101-auto", ""); err != nil {
		t.Fatalf("promote auto-approved: %v", err)
	}
	if es.EpisodePendingReview("20260101-auto") {
		t.Fatal("auto-approved episode pending after promote")
	}
}
