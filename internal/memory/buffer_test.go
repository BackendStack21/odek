package memory

import (
	"strings"
	"testing"
)

func TestBufferAppendAndLines(t *testing.T) {
	b := NewBuffer(5)
	b.Append("first line")
	b.Append("second line")

	lines := b.Lines()
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "first line" {
		t.Errorf("expected 'first line', got %q", lines[0])
	}
}

func TestBufferEviction(t *testing.T) {
	b := NewBuffer(3)
	b.Append("line 1")
	b.Append("line 2")
	b.Append("line 3")
	b.Append("line 4") // should evict line 1

	lines := b.Lines()
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "line 2" {
		t.Errorf("expected 'line 2' as oldest, got %q", lines[0])
	}
	if lines[2] != "line 4" {
		t.Errorf("expected 'line 4' as newest, got %q", lines[2])
	}
}

func TestBufferClear(t *testing.T) {
	b := NewBuffer(5)
	b.Append("something")
	b.Clear()

	lines := b.Lines()
	if len(lines) != 0 {
		t.Errorf("expected 0 lines after clear, got %d", len(lines))
	}
}

func TestBufferEmpty(t *testing.T) {
	b := NewBuffer(5)
	lines := b.Lines()
	if len(lines) != 0 {
		t.Errorf("expected 0 lines, got %d", len(lines))
	}
}

func TestBufferRestoreFromSlice(t *testing.T) {
	b := NewBuffer(5)
	b.Append("existing first")
	b.Append("existing second")

	saved := b.Lines()

	// New buffer, restore from saved
	b2 := NewBuffer(5)
	for _, line := range saved {
		b2.Append(line)
	}

	restored := b2.Lines()
	if len(restored) != 2 {
		t.Errorf("expected 2 lines, got %d", len(restored))
	}
	if restored[0] != "existing first" {
		t.Errorf("expected 'existing first', got %q", restored[0])
	}
}

func TestBufferRestoreWithEviction(t *testing.T) {
	// Simulate loading a full buffer from session
	b := NewBuffer(3)
	b.Append("old 1")
	b.Append("old 2")
	b.Append("old 3")

	// New session, new buffer, but we append the saved lines
	b2 := NewBuffer(3)
	for _, line := range b.Lines() {
		b2.Append(line)
	}

	// Add more — should evict oldest
	b2.Append("new 1")

	lines := b2.Lines()
	if len(lines) != 3 {
		t.Errorf("expected 3, got %d", len(lines))
	}
	if lines[0] != "old 2" {
		t.Errorf("expected old 2, got %q", lines[0])
	}
}

func TestBufferCapZero(t *testing.T) {
	b := NewBuffer(0)
	b.Append("should be discarded")
	lines := b.Lines()
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for 0 cap, got %d", len(lines))
	}
}

func TestBufferCap(t *testing.T) {
	b := NewBuffer(10)
	if b.Cap() != 10 {
		t.Errorf("expected cap 10, got %d", b.Cap())
	}
}

func TestBufferLen_Empty(t *testing.T) {
	b := NewBuffer(10)
	if b.Len() != 0 {
		t.Errorf("expected len 0, got %d", b.Len())
	}
}

func TestBufferLen_WithItems(t *testing.T) {
	b := NewBuffer(10)
	b.Append("a")
	b.Append("b")
	b.Append("c")
	if b.Len() != 3 {
		t.Errorf("expected len 3, got %d", b.Len())
	}
}

func TestBufferLen_AfterEviction(t *testing.T) {
	b := NewBuffer(3)
	b.Append("1")
	b.Append("2")
	b.Append("3")
	b.Append("4") // evicts 1, still 3 items
	if b.Len() != 3 {
		t.Errorf("expected len 3 after eviction, got %d", b.Len())
	}
}

func TestBufferLen_AfterClear(t *testing.T) {
	b := NewBuffer(5)
	b.Append("something")
	b.Clear()
	if b.Len() != 0 {
		t.Errorf("expected len 0 after clear, got %d", b.Len())
	}
}

func TestBufferNegativeCap(t *testing.T) {
	b := NewBuffer(-1)
	if b.Cap() != 0 {
		t.Errorf("expected cap 0 for negative input, got %d", b.Cap())
	}
	b.Append("should be discarded")
	if b.Len() != 0 {
		t.Errorf("expected len 0 for disabled buffer, got %d", b.Len())
	}
}

func TestBufferFormatLine(t *testing.T) {
	line := FormatBufferLine("user", "fix TOCTOU race")
	if !strings.Contains(line, "user") {
		t.Errorf("expected role in line, got %q", line)
	}
	if !strings.Contains(line, "fix TOCTOU") {
		t.Errorf("expected message in line, got %q", line)
	}
}

// ── sanitizeLine tests ───────────────────────────────────────────────

func TestSanitizeLine_Empty(t *testing.T) {
	got := sanitizeLine("")
	if got != "" {
		t.Errorf("sanitizeLine('') = %q, want ''", got)
	}
}

func TestSanitizeLine_NewlinesAndTabs(t *testing.T) {
	got := sanitizeLine("hello\nworld\rtest\t!")
	// \n, \r, \t each become spaces
	expected := "hello world test !"
	if got != expected {
		t.Errorf("sanitizeLine = %q, want %q", got, expected)
	}
}

func TestSanitizeLine_OnlySpecialChars(t *testing.T) {
	got := sanitizeLine("\n\r\t")
	expected := "   "
	if got != expected {
		t.Errorf("sanitizeLine = %q, want %q", got, expected)
	}
}

func TestSanitizeLine_Unicode(t *testing.T) {
	input := "hello 世界 ✓ — «test»"
	got := sanitizeLine(input)
	if got != input {
		t.Errorf("sanitizeLine should preserve unicode, got %q, want %q", got, input)
	}
}

func TestSanitizeLine_VeryLong(t *testing.T) {
	long := strings.Repeat("a", 10000)
	got := sanitizeLine(long)
	if len(got) != 10000 {
		t.Errorf("sanitizeLine length = %d, want %d", len(got), 10000)
	}
	if got != long {
		t.Errorf("sanitizeLine should preserve long strings unchanged")
	}
}

func TestSanitizeLine_MixedUnicodeAndNewlines(t *testing.T) {
	got := sanitizeLine("line1\nline2\r\n\t世界")
	// \n, \r, \n, \t → 4 spaces between "line2" and "世界"
	if got != "line1 line2   世界" {
		t.Errorf("sanitizeLine = %q, want %q", got, "line1 line2   世界")
	}
}

func TestSanitizeLine_AllWhitespace(t *testing.T) {
	got := sanitizeLine("   \t  \n  \r  ")
	// 3 spaces + \t(1) + 2 spaces + \n(1) + 2 spaces + \r(1) + 2 spaces = 12 spaces
	if got != "            " {
		t.Errorf("sanitizeLine = %q, want %q", got, "            ")
	}
}
