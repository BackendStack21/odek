package main

import (
	"strings"
	"testing"
)

// Regression (doc-contract): the head_tail tool description promised
// "Streaming — stops after N lines (no full-file read for head)", but
// readHead full-scans every file to report the exact total line count —
// and TestHeadTail_Head pins that exact total as intended behavior. The
// model (and every operator reading docs) was told the opposite of what
// the code does: a 10 MiB log previewed with lines=5 silently paid for
// a full scan. This test pins the description to the real contract:
// bounded memory (1 MiB line buffer, 1 MiB output cap), NOT a
// stopped-early scan.
func TestHeadTail_DescriptionMatchesBehavior(t *testing.T) {
	desc := (&headTailTool{}).Description()
	if strings.Contains(desc, "stops after N lines") || strings.Contains(desc, "no full-file read") {
		t.Fatalf("head_tail description still claims a streaming stop the implementation does not do: %q", desc)
	}
	if !strings.Contains(desc, "total") {
		t.Fatalf("head_tail description should mention that exact total line counts are reported: %q", desc)
	}
}
