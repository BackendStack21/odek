package render

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text the renderer prints comes from the model and from tools (a fetched
// page, a file, a command's output). A terminal treats some of those bytes as
// commands: CSI sequences move the cursor and erase lines, OSC sequences set
// the window title or forge hyperlinks, a carriage return or backspace
// overwrites what was printed, a bidi override reorders it, and invisible
// format characters hide text. Every model- or tool-sourced string goes
// through escapeTerminal before it is written, so the operator reads the
// bytes that were produced, not what they would make the terminal draw. The
// escapes match the approval prompts' (\x1b, \r, ‮, \xff).

// zwj is the zero-width joiner. Inside an emoji sequence it only selects a
// glyph, so it is kept there; anywhere else it hides a word boundary and is
// escaped like any other invisible character.
const zwj = '‍'

// Tag characters (U+E0020–U+E007F) are invisible. The only text that uses
// them is an emoji tag sequence: the black flag U+1F3F4, a subdivision code
// spelled in lowercase or digit tag characters, and the cancel tag U+E007F.
// Only the recommended (RGI) subdivision flags — England, Scotland, Wales —
// print raw. The tags after a black flag are held until the cancel tag shows
// whether they spell one of those; any other run, terminated or not, is
// escaped whole. Keeping arbitrary codes would let repeated flags carry
// invisible ASCII a few characters at a time.
const (
	blackFlag = 0x1F3F4
	cancelTag = 0xE007F
	// maxHeldTags bounds the held run: the longest RGI subdivision code.
	maxHeldTags = 5
)

// rgiSubdivisions are the subdivision codes of the RGI flag tag sequences.
var rgiSubdivisions = map[string]bool{"gbeng": true, "gbsct": true, "gbwls": true}

// escState is the escaping state carried from one stream fragment to the
// next: the last rune seen, and the tag characters held after a black flag
// until the cancel tag decides whether they print raw or escaped.
type escState struct {
	prev rune
	held [maxHeldTags]rune
	n    int
}

// flush returns the held tag run escaped and clears it: the run ended without
// completing an RGI flag (another character, the end of the text or a stream
// boundary).
func (st *escState) flush() string {
	if st.n == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range st.held[:st.n] {
		writeEscapedRune(&b, r)
	}
	st.n = 0
	return b.String()
}

// heldRGI reports whether the held tags spell an RGI subdivision code.
func (st *escState) heldRGI() bool {
	var code [maxHeldTags]byte
	for i, r := range st.held[:st.n] {
		code[i] = byte(r - 0xE0000)
	}
	return rgiSubdivisions[string(code[:st.n])]
}

// writeEscapedRune writes the visible escape for r.
func writeEscapedRune(b *strings.Builder, r rune) {
	if r > 0xFFFF {
		fmt.Fprintf(b, `\U%08x`, r)
	} else {
		fmt.Fprintf(b, `\u%04x`, r)
	}
}

// isTagSpec reports whether r is a tag character a subdivision code uses: a
// lowercase letter or a digit.
func isTagSpec(r rune) bool {
	return (r >= 0xE0030 && r <= 0xE0039) || (r >= 0xE0061 && r <= 0xE007A)
}

// needsEscape reports whether r is shown as an escape rather than printed.
func needsEscape(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f:
		return true
	case r < 0x80:
		return false
	case r <= 0x9f: // C1 controls, including the 8-bit CSI and OSC
		return true
	case r == ' ' || r == ' ': // line and paragraph separators
		return true
	case r == '͏', r == 'ᅟ', r == 'ᅠ', r == '᠎', r == 'ㅤ', r == 'ﾠ':
		return true // invisible joiner and fillers that render as blank space
	}
	return unicode.Is(unicode.Cf, r) // bidi controls, zero-width, tag characters, BOM
}

// escapeTerminal returns s with every control, bidi and invisible character
// replaced by a visible escape. With keepLayout, newline and tab are kept so
// multi-line text stays readable; otherwise they are escaped too. Invalid
// UTF-8 bytes are escaped as \xNN. The pass is linear and returns s itself
// when nothing needs escaping.
func escapeTerminal(s string, keepLayout bool) string {
	var st escState
	out := escapeTerminalFrom(s, keepLayout, &st)
	if st.n > 0 {
		out += st.flush()
	}
	return out
}

// escapeTerminalFrom is escapeTerminal continuing from the state st (zero at
// the start of a text), updating it so a stream can be escaped fragment by
// fragment with the same result as the joined text. Tag characters of an
// unfinished flag stay held in st; the stream flushes them (st.flush) at its
// boundary.
func escapeTerminalFrom(s string, keepLayout bool, st *escState) string {
	clean := st.n == 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c == 0x7f || (c < 0x20 && (!keepLayout || (c != '\n' && c != '\t'))) {
			clean = false
			break
		}
	}
	if clean {
		if s != "" {
			st.prev = rune(s[len(s)-1])
		}
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if st.n > 0 || st.prev == blackFlag {
			switch {
			case isTagSpec(r) && st.n < maxHeldTags:
				st.held[st.n] = r
				st.n++
				st.prev = r
				i += size
				continue
			case r == cancelTag && st.n > 0:
				if st.heldRGI() {
					for _, t := range st.held[:st.n] {
						b.WriteRune(t)
					}
					b.WriteRune(r)
					st.n = 0
				} else {
					b.WriteString(st.flush())
					writeEscapedRune(&b, r)
				}
				st.prev = r
				i += size
				continue
			default:
				b.WriteString(st.flush())
			}
		}
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t':
			if keepLayout {
				b.WriteRune(r)
			} else if r == '\n' {
				b.WriteString(`\n`)
			} else {
				b.WriteString(`\t`)
			}
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == zwj && isEmojiPart(st.prev):
			b.WriteRune(r)
		case needsEscape(r):
			writeEscapedRune(&b, r)
		default:
			b.WriteRune(r)
		}
		st.prev = r
		i += size
	}
	return b.String()
}

// isEmojiPart reports whether r can precede a joiner inside an emoji
// sequence: a pictographic symbol, a variation selector or a skin-tone
// modifier.
func isEmojiPart(r rune) bool {
	return (r >= 0x2190 && unicode.Is(unicode.So, r)) || r == '️' || (r >= 0x1f3fb && r <= 0x1f3ff)
}

// incompleteTail returns the index where a trailing, not yet complete UTF-8
// sequence starts in s, or len(s) when s ends on a rune boundary. A stream
// fragment can end in the middle of a multibyte character (or of a two-byte
// C1 control); that tail is held back until the next fragment completes it.
func incompleteTail(s string) int {
	for i := len(s) - 1; i >= 0 && i >= len(s)-utf8.UTFMax+1; i-- {
		if utf8.RuneStart(s[i]) {
			if s[i] >= 0x80 && !utf8.FullRuneInString(s[i:]) {
				return i
			}
			return len(s)
		}
	}
	return len(s)
}

// streamEscape escapes one stream fragment, completing a multibyte character
// held back from the previous fragment and holding back one that this
// fragment leaves incomplete.
func (r *Renderer) streamEscape(text string) string {
	if r.streamCarry != "" {
		text = r.streamCarry + text
		r.streamCarry = ""
	}
	cut := incompleteTail(text)
	r.streamCarry = text[cut:]
	return escapeTerminalFrom(text[:cut], true, &r.streamPrev)
}

// flushStreamCarry writes a held-back fragment tail that the stream never
// completed. It is invalid UTF-8 by then, so it is shown escaped.
//
// Tag characters held for an unfinished flag are flushed escaped too.
func (r *Renderer) flushStreamCarry() {
	if r.streamCarry == "" && r.streamPrev.n == 0 {
		return
	}
	carry := r.streamCarry
	r.streamCarry = ""
	style := ""
	if r.streamLastKind == 1 {
		style = dim + italic
	}
	out := escapeTerminalFrom(carry, true, &r.streamPrev) + r.streamPrev.flush()
	if style != "" {
		out = r.style(style, out)
	}
	fmt.Fprint(r.w, out)
}
