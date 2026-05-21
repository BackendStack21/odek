package telegram

import (
	"strings"
	"testing"
)

// ─── EscapeMarkdown ───────────────────────────────────────────────────────────

func TestEscapeMarkdown_SafeText(t *testing.T) {
	input := "Hello, World! This is safe text."
	got := EscapeMarkdown(input)
	// Note: '.' and '!' ARE reserved MarkdownV2 characters and get escaped.
	want := "Hello, World\\! This is safe text\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_AllReservedChars(t *testing.T) {
	input := "_*[]()~`>#+-=|{}.!"
	got := EscapeMarkdown(input)
	// Backtick starts an (unclosed) code span, so everything after it passes through unescaped.
	want := "\\_\\*\\[\\]\\(\\)\\~`>#+-=|{}.!"
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_ReservedCharsInContext(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "underscore in text",
			input: "hello_world",
			want:  "hello\\_world",
		},
		{
			name:  "asterisk in text",
			input: "bold*text",
			want:  "bold\\*text",
		},
		{
			name:  "brackets in text",
			input: "[link text]",
			want:  "\\[link text\\]",
		},
		{
			name:  "parentheses in text",
			input: "(parenthesis)",
			want:  "\\(parenthesis\\)",
		},
		{
			name:  "hash in text",
			input: "#header",
			want:  "\\#header",
		},
		{
			name:  "exclamation in text",
			input: "Hello!",
			want:  "Hello\\!",
		},
		{
			name:  "pipe in text",
			input: "a|b|c",
			want:  "a\\|b\\|c",
		},
		{
			name:  "dot in text",
			input: "3.14",
			want:  "3\\.14",
		},
		{
			name:  "minus in text",
			input: "item - list",
			want:  "item \\- list",
		},
		{
			name:  "plus in text",
			input: "a + b",
			want:  "a \\+ b",
		},
		{
			name:  "equals in text",
			input: "a = b",
			want:  "a \\= b",
		},
		{
			name:  "curly braces in text",
			input: "{key: value}",
			want:  "\\{key: value\\}",
		},
		{
			name:  "tilde in text",
			input: "~strikethrough~",
			want:  "\\~strikethrough\\~",
		},
		{
			name:  "greater than in text",
			input: "> quote",
			want:  "\\> quote",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeMarkdown(tt.input)
			if got != tt.want {
				t.Errorf("EscapeMarkdown(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeMarkdown_InlineCodeSpan(t *testing.T) {
	input := "This is `code with _special_ *chars*` outside."
	got := EscapeMarkdown(input)
	// Backticks are NOT escaped (they delimit code spans). Content inside is preserved.
	// The '.' at the end IS a reserved char and gets escaped.
	want := "This is `code with _special_ *chars*` outside\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_CodeBlock(t *testing.T) {
	input := "```\nfunc foo() {\n  _private := true\n  return _private\n}\n```"
	got := EscapeMarkdown(input)
	// Code blocks are passed through unchanged — backticks are NOT escaped.
	want := input
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_CodeSpanContentPreserved(t *testing.T) {
	input := "Use `some_func()` or `_private` for naming."
	got := EscapeMarkdown(input)
	// Backticks preserved, content inside backticks not escaped.
	// Only the '.' at the end gets escaped.
	want := "Use `some_func()` or `_private` for naming\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_MixedContent(t *testing.T) {
	input := "Normal _text_ with `code _inside_` and *more* chars."
	got := EscapeMarkdown(input)
	// Backticks preserved, content inside backticks not escaped.
	// Outside backticks: _, *, and . get escaped.
	want := "Normal \\_text\\_ with `code _inside_` and \\*more\\* chars\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_EmptyString(t *testing.T) {
	got := EscapeMarkdown("")
	if got != "" {
		t.Errorf("EscapeMarkdown(\"\") = %q, want \"\"", got)
	}
}

func TestEscapeMarkdown_Emoji(t *testing.T) {
	input := "Hello 🎉 World! This works _fine_."
	got := EscapeMarkdown(input)
	// '!', '_', and '.' are all reserved and get escaped.
	want := "Hello 🎉 World\\! This works \\_fine\\_\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_OnlyReservedChars(t *testing.T) {
	input := "_*"
	want := "\\_\\*"
	got := EscapeMarkdown(input)
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

func TestEscapeMarkdown_NoReservedChars(t *testing.T) {
	input := "Just plain alphanumeric text 123."
	got := EscapeMarkdown(input)
	// '.' is a reserved character and gets escaped.
	want := "Just plain alphanumeric text 123\\."
	if got != want {
		t.Errorf("EscapeMarkdown(%q) = %q, want %q", input, got, want)
	}
}

// ─── FormatResponse ───────────────────────────────────────────────────────────

func TestFormatResponse_PlainText(t *testing.T) {
	input := "Hello, World!"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// '!' is reserved and gets escaped by convertItalicAndEscape
	want := "Hello, World\\!"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_ReservedChars(t *testing.T) {
	input := "Price: $5.00 (discount!)"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// '.', '(', ')', '!' are all reserved chars — they get escaped
	want := "Price: $5\\.00 \\(discount\\!\\)"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_HeaderH2(t *testing.T) {
	input := "## Title Here"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "*Title Here*"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_HeaderH2WithReservedChars(t *testing.T) {
	input := "## Price: $5.00"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "*Price: $5\\.00*"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_HeaderH2WithItalic(t *testing.T) {
	input := "## Important: *note*"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// *note* becomes _note_, then wrapped in ** for header → *_note_*
	want := "*Important: _note_*"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_ItalicConversion(t *testing.T) {
	input := "This is *italic* text."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// *italic* becomes _italic_, '.' gets escaped
	want := "This is _italic_ text\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_BoldStaysBold(t *testing.T) {
	input := "This is **bold** text."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// **bold** stays as-is, '.' gets escaped
	want := "This is **bold** text\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_BoldAndItalic(t *testing.T) {
	input := "**bold** and *italic*"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "**bold** and _italic_"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_CodeBlockPreserved(t *testing.T) {
	input := "Before\n```\ncode _here_\n```\nAfter"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Code block content untouched (```...``` keeps everything as-is).
	// "Before" and "After" are normal lines — 'A' is not reserved so "After" stays.
	want := "Before\n```\ncode _here_\n```\nAfter"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_InlineCodePreserved(t *testing.T) {
	input := "Use `fmt.Println()` for output."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Backticks and content inside are preserved (not escaped, not italic-converted).
	// '.' at end gets escaped.
	want := "Use `fmt.Println()` for output\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_InlineCodeWithReservedCharsInside(t *testing.T) {
	input := "Use `_private_` and `*star*`."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Inside code span: content preserved as-is (no italic conversion, no escaping)
	// Outside: '.' gets escaped.
	// The backticks themselves are NOT escaped by convertItalicAndEscape.
	want := "Use `_private_` and `*star*`\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_SeparatorLine(t *testing.T) {
	input := "Text\n─────\nMore text"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "Text\n---\nMore text"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_SeparatorShortLine(t *testing.T) {
	input := "──"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Two ── chars are recognized as a separator and converted to ---
	want := "---"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_SeparatorWithSpaces(t *testing.T) {
	input := "Text\n ──── \nMore text"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "Text\n---\nMore text"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_PipeTable(t *testing.T) {
	input := "| Name | Age |\n|------|-----|\n| Alice | 30 |"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "```\n| Name | Age |\n|------|-----|\n| Alice | 30 |\n```"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_PipeTableWithSurroundingText(t *testing.T) {
	input := "Here is data:\n| A | B |\n| 1 | 2 |\nEnd."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Table gets wrapped in ```...```, '.' on "End." gets escaped
	want := "Here is data:\n```\n| A | B |\n| 1 | 2 |\n```\nEnd\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_MEDIALeftAsIs(t *testing.T) {
	input := "Some text\nMEDIA:photo:/path/to/image.jpg\nMore text"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// MEDIA line left as-is, other lines processed (no reserved chars in this input)
	want := "Some text\nMEDIA:photo:/path/to/image.jpg\nMore text"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_EmptyString(t *testing.T) {
	got, err := FormatResponse("")
	if err != nil {
		t.Fatalf("FormatResponse(\"\") returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 chunks for empty input, got %d chunks: %q", len(got), got)
	}
}

func TestFormatResponse_MultipleHeaders(t *testing.T) {
	input := "## First\n\n## Second\n\nSome text"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	want := "*First*\n\n*Second*\n\nSome text"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_MixedFormatting(t *testing.T) {
	input := "## Header\n\nThis is *italic* and **bold**.\n\n```\ncode block\n```\n\nNormal `inline code` here."
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Code blocks (```...```) pass through unchanged.
	// Inline code backticks preserved, content inside preserved.
	// '.' after "bold" gets escaped.
	want := "*Header*\n\nThis is _italic_ and **bold**\\.\n\n```\ncode block\n```\n\nNormal `inline code` here\\."
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_LongTextChunked(t *testing.T) {
	// Create text well over 4096 bytes with paragraph breaks
	para := strings.Repeat("This is a test paragraph. ", 50) // ~1250 bytes
	input := para + "\n\n" + para + "\n\n" + para + "\n\n" + para
	if len(input) <= 4096 {
		t.Fatalf("test input too short (%d bytes), need >4096", len(input))
	}
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(long text) returned error: %v", err)
	}
	if len(got) < 2 {
		t.Errorf("expected at least 2 chunks for >4096 byte input, got %d", len(got))
	}
	// Verify each chunk is within size limit
	for i, chunk := range got {
		// After markdown conversion, chunks may be slightly larger than the original
		// because reserved characters are escaped with backslashes
		if len(chunk) > 4100 {
			t.Errorf("chunk %d exceeds reasonable size: %d bytes", i, len(chunk))
		}
	}
	// Verify all chunks together reconstruct to a superset of the original content
	combined := strings.Join(got, "\n")
	for _, p := range strings.Split(input, "\n\n") {
		// Content may be escaped — check for a unique substring without punctuation
		short := strings.TrimSpace(p[:20])
		if !strings.Contains(combined, short) {
			t.Errorf("chunks missing paragraph content: %q", p[:50])
		}
	}
}

func TestFormatResponse_ExactBoundary(t *testing.T) {
	// Two paragraphs that together fit in 4096 bytes
	para1 := strings.Repeat("a", 2000)
	para2 := strings.Repeat("b", 2000)
	input := para1 + "\n\n" + para2 // 2000 + 2 + 2000 = 4002 bytes
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 chunk for 4002 byte input, got %d", len(got))
	}
}

func TestFormatResponse_SeparatorThenTableThenHeader(t *testing.T) {
	input := "Text\n─────\n| A | B |\n| 1 | 2 |\n## Summary"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Separator → ---, Table → wrapped in ```, Header → *Summary*
	want := "Text\n---\n```\n| A | B |\n| 1 | 2 |\n```\n*Summary*"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_NotH2(t *testing.T) {
	// Lines starting with "##" but not "## " should NOT be treated as headers
	input := "##Not a header"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// "##" gets escaped: \#\#Not a header
	want := "\\#\\#Not a header"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

func TestFormatResponse_CodeBlockTransition(t *testing.T) {
	input := "Text\n```\ncode\n```\nMore text"
	got, err := FormatResponse(input)
	if err != nil {
		t.Fatalf("FormatResponse(%q) returned error: %v", input, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	// Code block markers and content pass through unchanged
	want := "Text\n```\ncode\n```\nMore text"
	if got[0] != want {
		t.Errorf("FormatResponse(%q) = %q, want %q", input, got[0], want)
	}
}

// ─── splitChunks ──────────────────────────────────────────────────────────────

func TestSplitChunks_ShortText(t *testing.T) {
	input := "Short text under limit."
	got := splitChunks(input, 4096)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("splitChunks(%q) = %q, want %q", input, got[0], input)
	}
}

func TestSplitChunks_ExactBoundary(t *testing.T) {
	input := strings.Repeat("a", 4096) // exactly 4096 bytes
	got := splitChunks(input, 4096)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("splitChunks: content mismatch on exact boundary")
	}
}

func TestSplitChunks_JustOverBoundarySingleParagraph(t *testing.T) {
	// Single paragraph slightly over limit
	input := strings.Repeat("a", 4000) + " " + strings.Repeat("b", 200) // ~4200 bytes
	got := splitChunks(input, 4096)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(got))
	}
	for i, chunk := range got {
		if len(chunk) > 4096 {
			t.Errorf("chunk %d exceeds 4096 bytes: %d bytes", i, len(chunk))
		}
	}
	// Combined should preserve all content (whitespace at split point may differ)
	combined := strings.Join(got, "")
	if !strings.Contains(combined, strings.Repeat("a", 4000)) {
		t.Error("splitChunks lost 'a' content")
	}
	if !strings.Contains(combined, strings.Repeat("b", 200)) {
		t.Error("splitChunks lost 'b' content")
	}
}

func TestSplitChunks_OversizedParagraph(t *testing.T) {
	// Single paragraph way over limit, no spaces
	input := strings.Repeat("a", 10000) // 10000 bytes, no spaces
	got := splitChunks(input, 4096)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(got))
	}
	for i, chunk := range got {
		if len(chunk) > 4096 {
			t.Errorf("chunk %d exceeds 4096 bytes: %d bytes", i, len(chunk))
		}
	}
	combined := strings.Join(got, "")
	if combined != input {
		t.Errorf("splitChunks lost content: got %q, want %q", combined, input)
	}
}

func TestSplitChunks_SplitAtParagraph(t *testing.T) {
	// Two paragraphs where second pushes over limit
	para1 := strings.Repeat("a", 3000)
	para2 := strings.Repeat("b", 2000) // 3000+2000+2 = 5002 > 4096
	input := para1 + "\n\n" + para2
	got := splitChunks(input, 4096)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(got))
	}
	if got[0] != para1 {
		t.Errorf("first chunk should be para1, got %q", got[0])
	}
	if got[1] != para2 {
		t.Errorf("second chunk should be para2, got %q", got[1])
	}
}

func TestSplitChunks_EmptyString(t *testing.T) {
	got := splitChunks("", 4096)
	if len(got) != 0 {
		t.Errorf("expected 0 chunks for empty string, got %d", len(got))
	}
}

func TestSplitChunks_ZeroMaxBytes(t *testing.T) {
	input := "Hello, World!"
	got := splitChunks(input, 0) // should use default 4096
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("splitChunks with maxBytes=0: got %q, want %q", got[0], input)
	}
}

func TestSplitChunks_MultipleParagraphsWithinLimit(t *testing.T) {
	input := "Para one.\n\nPara two.\n\nPara three."
	got := splitChunks(input, 4096)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("splitChunks: got %q, want %q", got[0], input)
	}
}

func TestSplitChunks_MultipleParagraphsExceedingLimit(t *testing.T) {
	paraA := strings.Repeat("a", 3000)
	paraB := strings.Repeat("b", 1000)
	paraC := strings.Repeat("c", 1000)
	// paraA + \n\n + paraB = 3000+2+1000 = 4002 < 4096 → chunk 1
	// paraC = 1000 < 4096 → chunk 2
	input := paraA + "\n\n" + paraB + "\n\n" + paraC
	got := splitChunks(input, 4096)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(got))
	}
	expectedChunk1 := paraA + "\n\n" + paraB
	if got[0] != expectedChunk1 {
		t.Errorf("first chunk mismatch:\ngot:  %q\nwant: %q", got[0], expectedChunk1)
	}
	if got[1] != paraC {
		t.Errorf("second chunk mismatch:\ngot:  %q\nwant: %q", got[1], paraC)
	}
}

func TestSplitChunks_VeryLongParagraphWithSpaces(t *testing.T) {
	// Create a long paragraph with spaces that needs multiple splits
	words := make([]string, 1000)
	for i := range words {
		words[i] = "word"
	}
	input := strings.Join(words, " ") // ~5000 bytes with spaces
	got := splitChunks(input, 4096)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(got))
	}
	for i, chunk := range got {
		if len(chunk) > 4096 {
			t.Errorf("chunk %d exceeds 4096 bytes: %d bytes", i, len(chunk))
		}
	}
	// Should split at spaces
	combined := strings.Join(got, "")
	// Remove spaces that might have been trimmed
	combined = strings.ReplaceAll(combined, " ", "")
	inputNoSpaces := strings.ReplaceAll(input, " ", "")
	if combined != inputNoSpaces {
		t.Errorf("splitChunks lost content when splitting at spaces")
	}
}

func TestSplitChunks_SmallMaxBytes(t *testing.T) {
	input := "ab cd ef gh ij"
	got := splitChunks(input, 5)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks for maxBytes=5, got %d: %q", len(got), got)
	}
	for i, chunk := range got {
		if len(chunk) > 5 {
			t.Errorf("chunk %d exceeds 5 bytes: %d bytes: %q", i, len(chunk), chunk)
		}
	}
}

func TestSplitChunks_ParagraphLargerThanMaxBytesWithSpace(t *testing.T) {
	// Paragraph where splitAt finds a valid space
	input := strings.Repeat("a", 3000) + " " + strings.Repeat("b", 2000) // ~5001 bytes
	got := splitChunks(input, 4096)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(got))
	}
	for i, chunk := range got {
		if len(chunk) > 4096 {
			t.Errorf("chunk %d exceeds 4096 bytes: %d bytes", i, len(chunk))
		}
	}
}

func TestSplitChunks_NoSplitNeeded(t *testing.T) {
	input := strings.Repeat("x", 4000)
	got := splitChunks(input, 4096)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("content mismatch: got %q, want %q", got[0], input)
	}
}

func TestSplitChunks_ChunkBoundaryPreservesParagraphs(t *testing.T) {
	// Verify that paragraphs are NOT broken mid-way when they fit
	para1 := "Short paragraph."
	para2 := strings.Repeat("b", 4000) // big but under limit
	para3 := "Another short one."
	// para1 + para2 = 17+4000+2 = 4019 < 4096
	// para1 + para2 + para3 = 4019+2+20 = 4041 < 4096 → all fit
	input := para1 + "\n\n" + para2 + "\n\n" + para3
	got := splitChunks(input, 4096)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0] != input {
		t.Errorf("all paragraphs should fit in one chunk: got %q, want %q", got[0], input)
	}
}
