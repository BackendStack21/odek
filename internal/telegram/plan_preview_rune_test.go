package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Plan previews are truncated; truncation must not emit invalid UTF-8.
func TestRED_PlanPreviewSplitsRune(t *testing.T) {
	got := firstLine(strings.Repeat("é", 100), 79)
	if !utf8.ValidString(got) {
		t.Fatalf("firstLine produced invalid UTF-8: %q", got)
	}
}

func TestFirstLine_TruncationBoundaries(t *testing.T) {
	if got := firstLine("# Title\nbody", 80); got != "Title" {
		t.Errorf("heading = %q", got)
	}
	got := firstLine(strings.Repeat("a", 100), 10)
	if got != strings.Repeat("a", 10)+"…" {
		t.Errorf("ascii truncation = %q", got)
	}
	got = firstLine(strings.Repeat("é", 100), 79)
	if !strings.HasSuffix(got, "…") || len(got) > 79+len("…") {
		t.Errorf("multibyte truncation = %q (%d bytes)", got, len(got))
	}
}
