package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Snapshot truncation must back up to a UTF-8 rune boundary so multibyte
// characters cut by the cap never ship U+FFFD mojibake.
func TestTruncatePageContent_RuneBoundary(t *testing.T) {
	// Build content where the cap lands mid-rune: 'é' is 2 bytes.
	unit := strings.Repeat("a", 9) + "é"
	var b strings.Builder
	for b.Len() < maxBrowserSnapshotBytes {
		b.WriteString(unit)
	}
	content := b.String()
	got := truncatePageContent(content)
	trimmed := strings.TrimSuffix(got, "\n[content truncated: exceeds per-snapshot byte cap]")
	if !utf8.ValidString(trimmed) {
		t.Fatal("truncated content is not valid UTF-8")
	}
	if strings.ContainsRune(trimmed, utf8.RuneError) {
		t.Fatal("truncated content contains U+FFFD replacement rune")
	}
	if len(trimmed) > maxBrowserSnapshotBytes {
		t.Fatalf("truncated content %d bytes exceeds cap %d", len(trimmed), maxBrowserSnapshotBytes)
	}
}
