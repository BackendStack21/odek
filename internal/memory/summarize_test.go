package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeForBuffer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"short plain passthrough", "fixed the login bug", "fixed the login bug"},
		{"all code becomes placeholder", "```go\nfunc main() {}\n```", codePlaceholder},
		{
			"code plus prose keeps prose",
			"Here is the fix:\n```go\nx := 1\n```\nIt resolves the panic.",
			"Here is the fix: It resolves the panic.",
		},
		{
			"markdown stripped",
			"# Heading\n- **bold** item\n- [link](http://x) here",
			"Heading bold item link here",
		},
		{
			"inline code unwrapped",
			"call `doThing()` then `cleanup()`",
			"call doThing() then cleanup()",
		},
		{
			"leading filler dropped",
			"Sure, I'll help with that. Let me read the config file first.",
			"Let me read the config file first.",
		},
		{
			"filler only is kept",
			"Sure, I'll help with that.",
			"Sure, I'll help with that.",
		},
		{
			"internal newlines and tabs collapse",
			"line one\n\tline two\r\nline three",
			"line one line two line three",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeForBuffer(tc.in)
			if got != tc.want {
				t.Errorf("summarizeForBuffer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSummarizeForBuffer_SentenceBoundaryTruncation(t *testing.T) {
	// Many short sentences; expect a cut at a sentence boundary with an ellipsis,
	// within the rune cap.
	in := strings.Repeat("This is a sentence. ", 40) // 800 chars
	got := summarizeForBuffer(in)

	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if n := utf8.RuneCountInString(got); n > maxBufferSummaryRunes+1 { // +1 for the ellipsis rune
		t.Errorf("excerpt rune count %d exceeds cap %d (+1)", n, maxBufferSummaryRunes)
	}
	// Boundary cut: the text before the ellipsis should end with a sentence
	// terminator (no mid-sentence chop).
	trimmed := strings.TrimSuffix(got, "…")
	if !strings.HasSuffix(trimmed, ".") {
		t.Errorf("expected sentence-boundary cut, got %q", got)
	}
}

func TestSummarizeForBuffer_HardCutSingleLongToken(t *testing.T) {
	in := strings.Repeat("a", 500) // no spaces, no sentence ends
	got := summarizeForBuffer(in)

	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if n := utf8.RuneCountInString(body); n != maxBufferSummaryRunes {
		t.Errorf("hard cut body = %d runes, want exactly %d", n, maxBufferSummaryRunes)
	}
}

func TestSummarizeForBuffer_MultibyteBoundarySafe(t *testing.T) {
	// A long run of 3-byte runes with no spaces/sentence ends: must hard-cut on a
	// rune boundary, never splitting a rune.
	in := strings.Repeat("世", 500)
	got := summarizeForBuffer(in)

	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8 (split a rune): %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if n := utf8.RuneCountInString(body); n > maxBufferSummaryRunes {
		t.Errorf("excerpt body = %d runes, exceeds cap %d", n, maxBufferSummaryRunes)
	}
}
