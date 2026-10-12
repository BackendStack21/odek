package danger

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Approval prompts show model-supplied text (a command, a description, a path,
// a tool argument). Terminal escape sequences, carriage returns, backspaces
// and Unicode bidirectional or invisible format characters can make the human
// read a different command from the one that runs: a cursor-movement sequence
// overwrites the visible line, a right-to-left override reorders it, a
// zero-width character splits a word the reader thinks is intact. Everything
// shown in an approval prompt goes through SanitizeForDisplay or
// SanitizeInline first.

const (
	// DisplayMaxBytes caps the input bytes a prompt field displays.
	DisplayMaxBytes = 32 << 10
	// InlineMaxBytes caps single-line fields (descriptions, tool names, batch
	// card items, config values).
	InlineMaxBytes = 4 << 10

	// displayTailBytes of a capped value are always kept: an over-long
	// command must not hide its payload behind a benign head.
	displayTailBytes = 1 << 10
)

// SanitizeForDisplay returns s in a form that is safe to print in an approval
// prompt or render as text:
//
//   - C0 and C1 control characters, DEL, carriage return, invalid UTF-8 and
//     every character that is not printable (Unicode format characters
//     including bidi controls and isolates, zero-width and Hangul filler
//     characters, line and paragraph separators, non-ASCII spaces) are
//     replaced by a visible escape: \x1b, \r, \u202e, \xff.
//   - Newline and tab are kept as they are, so a multi-line command stays
//     readable. Callers that print into a line-oriented layout indent
//     continuation lines; SanitizeInline escapes them instead.
//   - A value longer than DisplayMaxBytes is shortened to its head and its
//     last kilobyte joined by an explicit "…[N more bytes]" marker, cut on
//     rune boundaries. The cap counts input bytes; escapes can make the
//     output longer.
func SanitizeForDisplay(s string) string {
	return sanitizeDisplay(s, DisplayMaxBytes, true)
}

// SanitizeInline is SanitizeForDisplay for single-line fields: newline and
// tab are escaped as \n and \t too, and the cap is InlineMaxBytes.
func SanitizeInline(s string) string {
	return sanitizeDisplay(s, InlineMaxBytes, false)
}

func sanitizeDisplay(s string, max int, keepLayout bool) string {
	if s == "" {
		return ""
	}
	if len(s) <= max {
		return escapeForDisplay(s, keepLayout)
	}
	tail := displayTailBytes
	if tail > max/4 {
		tail = max / 4
	}
	head := max - tail
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tailStart := len(s) - tail
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	return escapeForDisplay(s[:head], keepLayout) +
		fmt.Sprintf("…[%d more bytes]", tailStart-head) +
		escapeForDisplay(s[tailStart:], keepLayout)
}

func escapeForDisplay(s string, keepLayout bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			if keepLayout {
				b.WriteByte('\n')
			} else {
				b.WriteString(`\n`)
			}
		case r == '\t':
			if keepLayout {
				b.WriteByte('\t')
			} else {
				b.WriteString(`\t`)
			}
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == ' ':
			b.WriteByte(' ')
		case unicode.IsPrint(r) && !isInvisible(r) && !isZeroWidthMark(r):
			b.WriteRune(r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	return b.String()
}

// promptContinuationIndent aligns the lines of a multi-line value under the
// label of its prompt field, so command text can never open a line that reads
// as a field of its own.
const promptContinuationIndent = "         "

// indentContinuation prefixes every line after the first with the prompt
// continuation indent.
func indentContinuation(s string) string {
	return strings.ReplaceAll(s, "\n", "\n"+promptContinuationIndent)
}

// formatApprovalPrompt renders the Risk/Run/Why block of the terminal
// approval prompt from sanitized fields.
func formatApprovalPrompt(cls RiskClass, cmd, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n⚠️  \033[1mRisk:\033[0m  %s\n", SanitizeInline(string(cls)))
	fmt.Fprintf(&b, "   \033[1mRun:\033[0m  %s\n", indentContinuation(SanitizeForDisplay(cmd)))
	if description != "" {
		fmt.Fprintf(&b, "   \033[1mWhy:\033[0m  %s\n", indentContinuation(SanitizeInline(description)))
	}
	return b.String()
}

// IsZeroWidthMark reports whether r is a zero-width nonspacing mark (variation
// selector, Mongolian free variation selector, Khmer inherent vowel, Braille
// blank, object replacement) that a display must escape. Other renderers share
// this predicate so every surface hides the same set.
func IsZeroWidthMark(r rune) bool { return isZeroWidthMark(r) }

// isZeroWidthMark reports whether r is a nonspacing mark that renders with no
// width of its own: the variation selectors (U+FE00–FE0F, U+E0100–E01EF), the
// Mongolian free variation selectors and the Khmer inherent vowels. Unlike an
// accent they change nothing a reader can see, so a prompt must escape them.
func isZeroWidthMark(r rune) bool {
	switch {
	case r >= 0xFE00 && r <= 0xFE0F,
		r >= 0xE0100 && r <= 0xE01EF,
		r >= 0x180B && r <= 0x180D,
		r == 0x17B4 || r == 0x17B5,
		r == 0x180F, r == 0x2800, r == 0xFFFC:
		return true
	}
	return false
}
