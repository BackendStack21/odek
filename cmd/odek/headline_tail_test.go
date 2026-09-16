package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// ── B1: sub-agent headline keeps the TAIL ────────────────────────────────
//
// The old head-keep truncation chopped exactly the part that matters: a
// verdict/recommendation lives at the END of a report, so a 2048-rune
// head-cut dropped conclusions while keeping boilerplate. The headline cut
// must keep the tail (verdicts, file lists, next actions) with a leading
// ellipsis marking the trim.

func TestExtractSummaryInfo_KeepsTail(t *testing.T) {
	verdict := "VERDICT: MERGE — the patch is correct and complete."
	filler := strings.Repeat("Analysis prose. ", 200) // > 2048 runes
	content := filler + "\n\n" + verdict

	msgs := []session.Message{{Role: "assistant", Content: content}}
	got, total, truncated := extractSummaryInfo(msgs)

	if !truncated {
		t.Fatal("expected truncated=true for over-cap content")
	}
	if total != len([]rune(content)) {
		t.Errorf("total rune count = %d, want %d", total, len([]rune(content)))
	}
	if !strings.Contains(got, verdict) {
		t.Errorf("tail-keep headline lost the verdict — head-keep truncation drops conclusions:\n%.300s", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("tail-keep headline must mark the cut with a LEADING ellipsis, got: %.50s", got)
	}
	runes := len([]rune(got))
	if runes > subagentHeadlineMaxRunes+1 { // +1 for the ellipsis rune
		t.Errorf("headline = %d runes, want ≤ %d (+ellipsis)", runes, subagentHeadlineMaxRunes)
	}
}

func TestExtractSummaryInfo_ShortContentUntouched(t *testing.T) {
	msgs := []session.Message{{Role: "assistant", Content: "short answer"}}
	got, total, truncated := extractSummaryInfo(msgs)
	if truncated || total != 12 || got != "short answer" {
		t.Errorf("short content must pass through untouched, got %q (total=%d, truncated=%v)", got, total, truncated)
	}
}

// The parent-facing description must describe the new semantics.
func TestSubagentDescription_TailKeepSemantics(t *testing.T) {
	desc := (&delegateTasksTool{}).Description()
	if strings.Contains(desc, "a trailing … means it was cut") {
		t.Error("description still claims head-keep semantics ('a trailing … means it was cut')")
	}
	if !strings.Contains(desc, "leading …") {
		t.Error("description must state the tail-keep contract ('a leading … marks the cut; the end of the answer is kept')")
	}
}
