package main

// Bug-sweep batch 2 (fix/bug-hunt-b2) — B6 regression test.
//
// RED-first: redactGoal truncated by BYTES (goal[:2048]) while the constant
// is named GoalChars — a multi-byte rune at the boundary was split,
// corrupting the goal text with an invalid UTF-8 sequence exactly when the
// clamp engaged (long goals are the normal case for real tasks).

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactGoal_TruncationIsRuneSafe(t *testing.T) {
	// 2041 ASCII runes followed by multi-byte runes straddling byte 2048:
	// each 🚀 is 4 bytes, so byte-index 2048 lands INSIDE the second
	// rocket (bytes 2045-2048), splitting it mid-rune.
	goal := strings.Repeat("a", 2041) + strings.Repeat("🚀", 100)

	got := redactGoal(goal)

	if !utf8.ValidString(got) {
		t.Fatalf("redactGoal produced invalid UTF-8 (byte-sliced a multi-byte rune)")
	}
	if n := utf8.RuneCountInString(got); n > maxSubagentRegistryGoalChars {
		t.Fatalf("redactGoal returned %d runes, want <= %d", n, maxSubagentRegistryGoalChars)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 2040)) {
		t.Fatalf("redactGoal mangled the ASCII prefix")
	}
}
