package render

import (
	"bytes"
	"strings"
	"testing"
)

// The renderer escapes tag characters (U+E0020–E007F) because they smuggle
// invisible text, but not the supplementary variation selectors
// (U+E0100–E01EF), which carry arbitrary bytes the same way ("emoji
// smuggling": one selector per byte after a visible base). It also prints
// the other zero-width marks the approval display escapes (Khmer inherent
// vowels U+17B4/17B5, Mongolian FVS U+180B–180D, Braille blank U+2800).
// sanitize.go says its escapes match the approval prompts'; for these runes
// they do not, so a final answer or tool line can carry text the operator
// cannot see.
func TestRED_RendererPrintsInvisibleVariationSelectorsRaw(t *testing.T) {
	hidden := "Done\U000E0149\U000E0147\U000E015E឴᠋⠀."
	var buf bytes.Buffer
	New(&buf, false).FinalAnswer(hidden)
	out := buf.String()
	for _, r := range []rune{0xE0149, 0xE0147, 0xE015E, 0x17B4, 0x180B, 0x2800} {
		if strings.ContainsRune(out, r) {
			t.Errorf("final answer printed invisible rune U+%04X raw: %+q", r, out)
		}
	}
}

// The emoji presentation selector stays when it directly follows its symbol
// and is escaped everywhere else (chains and after plain text).
func TestRED_RendererKeepsEmojiPresentationSelectorOnly(t *testing.T) {
	if got := escapeTerminal("\u26a0\uFE0F ok", true); got != "\u26a0\uFE0F ok" {
		t.Errorf("emoji presentation selector escaped: %+q", got)
	}
	if got := escapeTerminal("a\uFE0Fb", true); strings.ContainsRune(got, 0xFE0F) {
		t.Errorf("stray selector printed raw: %+q", got)
	}
	if got := escapeTerminal("\u26a0\uFE0F\uFE0F\uFE0F", true); strings.Count(got, "\uFE0F") > 1 {
		t.Errorf("selector chain printed raw: %+q", got)
	}
}
