package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
)

func TestRED_EpisodePromote_RequiresSummaryHash(t *testing.T) {
	dir := newTestMemoryDir(t)
	writeReviewEpisode(t, dir, "20260201-rev4")
	req := httptest.NewRequest(http.MethodPost, "/api/memory/episodes/promote", strings.NewReader(`{"session_id":"20260201-rev4"}`))
	w := httptest.NewRecorder()
	handleMemoryEpisodePromote(dir)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("promote without summary_sha256 = %d, want 400 (%s)", w.Code, w.Body.String())
	}
	if !memory.NewEpisodeStore(dir, nil).EpisodePendingReview("20260201-rev4") {
		t.Fatal("episode promoted without a reviewed hash")
	}
}

func TestRED_MemoryCmd_ShowsFullSanitisedSummary(t *testing.T) {
	home := setupTestHome(t)
	dir := filepath.Join(home, ".odek", "memory")
	writeReviewEpisode(t, dir, "20260201-cli1")
	list := captureStdout(func() {
		if err := memoryCmd([]string{"list"}); err != nil {
			t.Error(err)
		}
	})
	promote := captureStdout(func() {
		if err := memoryCmd([]string{"promote", "20260201-cli1"}); err != nil {
			t.Error(err)
		}
	})
	for name, out := range map[string]string{"list": list, "promote": promote} {
		if !strings.Contains(out, "TAIL-INSTRUCTION-MARKER") {
			t.Errorf("memory %s does not show the full summary:\n%s", name, out)
		}
		if strings.ContainsAny(out, "\x1b‮") {
			t.Errorf("memory %s prints unsanitised summary: %q", name, out)
		}
	}
}
