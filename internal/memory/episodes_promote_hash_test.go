package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func episodeHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestRED_PromoteIfHash_ComparesUnderLockAndReturnsText(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	const text = "reviewed episode text"
	if err := es.WriteWithProvenance("20260301-ph", text, 4, EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	stored, err := es.Read("20260301-ph")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := es.PromoteIfHash("20260301-ph", episodeHash("something else")); !errors.Is(err, ErrEpisodeSummaryChanged) {
		t.Fatalf("mismatched hash err = %v, want ErrEpisodeSummaryChanged", err)
	}
	if !es.EpisodePendingReview("20260301-ph") {
		t.Fatal("episode promoted despite a hash mismatch")
	}

	got, err := es.PromoteIfHash("20260301-ph", episodeHash(stored))
	if err != nil {
		t.Fatalf("matching hash: %v", err)
	}
	if got != stored {
		t.Errorf("promoted text = %q, want %q", got, stored)
	}
	if es.EpisodePendingReview("20260301-ph") {
		t.Error("episode still pending after a matching promote")
	}
}

func TestRED_PromoteIfHash_UnreadableEpisodeIsNotPromoted(t *testing.T) {
	dir := t.TempDir()
	es := NewEpisodeStore(dir, nil)
	if err := es.WriteWithProvenance("20260301-gone", "text", 4, EpisodeProvenance{Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "20260301-gone.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := es.PromoteIfHash("20260301-gone", ""); err == nil {
		t.Fatal("an episode whose text cannot be read was promoted")
	}
	if !es.EpisodePendingReview("20260301-gone") {
		t.Fatal("unreadable episode was promoted")
	}
}
