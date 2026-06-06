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

// TestSummarizeForBuffer_EarlyAbbreviationNoCollapse guards against an early,
// lone sentence terminator (abbreviation / version / domain) collapsing the
// excerpt to a few runes. The cut must fall back to the word boundary near the
// cap and retain most of the content.
func TestSummarizeForBuffer_EarlyAbbreviationNoCollapse(t *testing.T) {
	cases := []string{
		"e.g., " + strings.Repeat("we should refactor the module ", 20),
		"node.js " + strings.Repeat("is a runtime for building scalable apps ", 20),
		"v1.2 " + strings.Repeat("introduces many features and improvements ", 20),
	}
	for _, in := range cases {
		got := summarizeForBuffer(in)
		if n := utf8.RuneCountInString(got); n < maxBufferSummaryRunes/2 {
			t.Errorf("early-abbreviation excerpt collapsed to %d runes: %q", n, got)
		}
	}
}

// TestSummarizeForBuffer_UnclosedFence ensures an unclosed code fence leaves no
// stray backticks in the summary.
func TestSummarizeForBuffer_UnclosedFence(t *testing.T) {
	in := "Here is some code:\n```go\nfunc main() { panic() }\nand more text after"
	got := summarizeForBuffer(in)
	if strings.Contains(got, "`") {
		t.Errorf("summary contains a leftover backtick: %q", got)
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
