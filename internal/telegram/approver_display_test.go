package telegram

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// The Telegram approval text shows the command and description with control
// and bidi characters made visible.
func TestBuildApprovalText_SanitizesCommandAndDescription(t *testing.T) {
	hostile := "echo ok\x1b[2K\r\x1b]0;safe\x07 \u202egnirts\u202c \u2066x\u2069 \u200bz"
	text := buildApprovalText(danger.NetworkEgress, hostile, "why "+hostile)
	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x200b ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Fatalf("approval text holds raw control or bidi character U+%04X: %q", r, text)
		}
	}
	if !strings.Contains(text, `x1b`) || !strings.Contains(text, `u202e`) {
		t.Errorf("escapes not visible in %q", text)
	}
}
