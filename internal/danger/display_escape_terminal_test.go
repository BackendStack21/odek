package danger

import (
	"strings"
	"testing"
)

func TestEscapeTerminalTextNeverShortens(t *testing.T) {
	long := strings.Repeat("a", DisplayMaxBytes*3) + "\x1b]52;c;ZXZpbA==\x07" + "\u202e"
	got := EscapeTerminalText(long)
	if strings.Contains(got, "more bytes]") {
		t.Fatalf("EscapeTerminalText shortened its input")
	}
	if strings.ContainsAny(got, "\x1b\x07\u202e") {
		t.Fatalf("control or bidi character survived: %q", got[len(got)-40:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", DisplayMaxBytes*3)) {
		t.Fatalf("plain text changed")
	}
	if EscapeTerminalText("line one\n\tline two") != "line one\n\tline two" {
		t.Fatalf("newline and tab must be kept")
	}
}
