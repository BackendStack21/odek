package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/session"
)

// Regression: classification ran PartialSummaryReason on the TAIL-KEPT
// summary. Tail-keep drops the HEAD, and the engine PREPENDS the marker —
// so an over-cap genuine partial summary was classified as success. The
// classification must run on the raw final assistant content.
func TestPartialClassificationUsesRawContent(t *testing.T) {
	marker := "[Iteration budget reached — partial summary]"
	// Engine reality: the marker is PREPENDED to the partial summary, and a
	// long tool-log body follows. Tail-keep of 2048 runes preserves the END
	// (body), not the marker — the truncated summary is blind to it.
	long := marker + "\n" + strings.Repeat("x", subagentHeadlineMaxRunes+50)
	msgs := []session.Message{{Role: "assistant", Content: long}}

	// The tail-kept summary alone does NOT carry the marker — proof the
	// old classification path was blind to it.
	summary, _, _ := extractSummaryInfo(msgs)
	if _, ok := loop.PartialSummaryReason(summary); ok {
		t.Fatal("premise broken: truncated summary still carries the marker")
	}

	raw := rawFinalAssistant(msgs)
	reason, ok := loop.PartialSummaryReason(raw)
	if !ok || reason != "iteration_budget" {
		t.Fatalf("raw-content classification = (%q, %v), want (iteration_budget, true)", reason, ok)
	}
}

func TestRawFinalAssistantEmpty(t *testing.T) {
	if got := rawFinalAssistant(nil); got != "" {
		t.Fatalf("rawFinalAssistant(nil) = %q, want empty", got)
	}
}
