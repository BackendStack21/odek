package loop

import (
	"strings"
	"testing"
)

// A batch approval card line shows tool name and resource with control and
// bidi characters made visible, and a multi-line resource cannot start a
// second numbered item.
func TestBatchApprovalLine_Sanitized(t *testing.T) {
	line := batchApprovalLine(0, "shell\x1b[2K", "ls\n  2. `shell` — `true`\x1b]0;x\x07 \u202egnirts")
	for _, r := range line {
		if r == '\n' && line[len(line)-1] != '\n' {
			t.Fatalf("resource text injected a line break: %q", line)
		}
		if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x202a && r <= 0x202e) {
			t.Fatalf("batch line holds raw control or bidi character U+%04X: %q", r, line)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Errorf("expected a single line, got %q", line)
	}
}
