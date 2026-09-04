package render

import (
	"bytes"
	"strings"
	"testing"
)

// ToolResult's truncation counts runes but the ellipsis gate counts bytes:
// a 41-rune CJK line (123 bytes, within the 120-rune limit) got a spurious
// " …", and a >120-rune multibyte line got a DOUBLE ellipsis (truncate's
// own + the gate's).
func TestToolResult_RuneConsistentEllipsis(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, true)

	// 41 runes, 123 bytes: within the 120-RUNE budget — no ellipsis.
	r.ToolResult(strings.Repeat("京", 41))
	out := buf.String()
	buf.Reset()
	if strings.HasSuffix(strings.TrimSpace(out), "…") {
		t.Errorf("spurious ellipsis on a 41-rune (123-byte) line within the rune budget: %q", out)
	}

	// 121 runes: truncated once by truncate() — exactly one ellipsis.
	r.ToolResult(strings.Repeat("京", 121))
	out = buf.String()
	if strings.Count(out, "…") > 1 {
		t.Errorf("double ellipsis on a >120-rune line: %q…", out[:40])
	}
}
