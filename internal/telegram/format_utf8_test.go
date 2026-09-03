package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A single spaceless paragraph longer than the 4096-byte message cap
// (CJK text, URLs, base64) must still split on rune boundaries: the old
// byte cut produced invalid UTF-8 chunks, which the Telegram API coerces
// to U+FFFD — the user receives corrupted text.
func TestFormatResponse_ChunksStayValidUTF8(t *testing.T) {
	long := strings.Repeat("日本語のテキストです。", 1200) // ~27 KB, no spaces
	chunks, err := FormatResponse(long)
	if err != nil {
		t.Fatalf("FormatResponse: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is invalid UTF-8 (mid-rune split): %q…", i, c[:min(40, len(c))])
		}
		if !strings.Contains(c, "日") {
			t.Fatalf("chunk %d lost its content: %q…", i, c[:min(40, len(c))])
		}
	}
}

// truncateStr must never cut mid-rune: a multibyte task title truncated
// at a byte boundary ships mojibake (U+FFFD after JSON encoding).
func TestTruncateStr_RuneSafe(t *testing.T) {
	// 日 is 3 bytes: cutting at 50 bytes lands mid-rune.
	s := strings.Repeat("日", 40) // 120 bytes
	got := truncateStr(s, 50)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateStr produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateStr must mark truncation with ellipsis, got %q", got)
	}
}
