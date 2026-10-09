package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateForIndex(t *testing.T) {
	if got := truncateForIndex("short"); got != "short" {
		t.Fatalf("short summary changed: %q", got)
	}
	got := truncateForIndex(strings.Repeat("a", 200))
	if len(got) != 120 || !strings.HasSuffix(got, "...") {
		t.Fatalf("ascii truncation: len %d", len(got))
	}
	got = truncateForIndex(strings.Repeat("日", 100))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") {
		t.Fatalf("multibyte truncation invalid: %q", got)
	}
}
