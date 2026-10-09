package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Index summary truncation must stay on a rune boundary.
func TestRED_IndexSummaryTruncationRuneSafe(t *testing.T) {
	es := NewEpisodeStore(t.TempDir(), nil)
	summary := strings.Repeat("é", 200)
	if err := es.Write("sess-1", summary, 5); err != nil {
		t.Fatal(err)
	}
	idx, err := es.ReadIndex()
	if err != nil || len(idx) != 1 {
		t.Fatal(err, idx)
	}
	if !utf8.ValidString(idx[0].Summary) || strings.ContainsRune(idx[0].Summary, utf8.RuneError) {
		t.Fatalf("index summary has a mid-rune cut: %q", idx[0].Summary)
	}
}
