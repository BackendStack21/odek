package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
)

func memresSetup(t *testing.T, id string) (*memory.EpisodeStore, string) {
	t.Helper()
	home := setupTestHome(t)
	dir := filepath.Join(home, ".odek", "memory")
	writeReviewEpisode(t, dir, id)
	store := memory.NewEpisodeStore(dir, nil)
	full, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return store, episodeSummaryHash(full)
}

// The CLI promote must be able to bind approval to the reviewed text, and
// refuse (promoting nothing) when the stored text no longer matches.
func TestRED_MemoryCmd_PromoteChecksReviewedHash(t *testing.T) {
	const id = "20260201-memres1"
	store, hash := memresSetup(t, id)

	wrong := strings.Repeat("0", len(hash))
	err := memoryCmd([]string{"promote", id, "--sha256", wrong})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("promote with stale hash: %v; want summary-changed error", err)
	}
	if !store.EpisodePendingReview(id) {
		t.Fatal("episode promoted despite hash mismatch")
	}
	captureStdout(func() {
		if err := memoryCmd([]string{"promote", id, "--sha256", hash}); err != nil {
			t.Errorf("promote with matching hash: %v", err)
		}
	})
	if store.EpisodePendingReview(id) {
		t.Fatal("episode still pending after promote with matching hash")
	}
}

func TestRED_MemoryCmd_ListPrintsReviewHash(t *testing.T) {
	const id = "20260201-memres2"
	_, hash := memresSetup(t, id)
	out := captureStdout(func() {
		if err := memoryCmd([]string{"list"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, hash) {
		t.Errorf("list output lacks the review hash %s:\n%s", hash, out)
	}
}

func TestRED_MemoryCmd_PromoteHashNeedsSingleID(t *testing.T) {
	const id = "20260201-memres3"
	store, hash := memresSetup(t, id)
	if err := memoryCmd([]string{"promote", id, "other-id", "--sha256", hash}); err == nil {
		t.Fatal("--sha256 with several ids must be refused")
	}
	if !store.EpisodePendingReview(id) {
		t.Fatal("episode promoted by a refused command")
	}
	if err := memoryCmd([]string{"promote", "--sha256"}); err == nil {
		t.Fatal("dangling --sha256 must be refused")
	}
}
