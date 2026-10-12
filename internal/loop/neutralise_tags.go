package loop

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

const (
	// tagMaxSeparators bounds the characters accepted between the two words
	// of the tag name.
	tagMaxSeparators = 8
)

// NeutraliseUntrustedLiterals makes a body unable to contain anything that
// reads as an untrusted-content boundary tag. The exact spelling
// "untrusted_content" is rewritten to "untrusted·content"; in addition every
// '<' (or fullwidth '＜') that begins a look-alike of the tag — optional '/',
// the words "untrusted" and "content" in any case, confusable script,
// fullwidth or styled letters, with invisible padding and a few separator
// characters between the words — becomes '‹', so a forged close tag cannot
// read as the real boundary closing. Names that merely continue the words
// (`<untrusted-content-policy>`, `List<Untrusted> content`) are left alone.
// Padding is unbounded: skipping an invisible rune is constant work, and a
// candidate is abandoned at its first mismatching rune, so the scan stays
// linear.
func NeutraliseUntrustedLiterals(s string) string {
	s = strings.ReplaceAll(s, "untrusted_content", "untrusted·content")
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if tagOpener(r) && tagLookalikeAt(s[i+sz:]) {
			b.WriteString(s[last:i])
			b.WriteString("‹")
			last = i + sz
		}
		i += sz
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// tagOpener reports whether r is, or renders as, a left angle bracket. U+2039
// is the neutralised form itself and is not an opener.
func tagOpener(r rune) bool {
	switch r {
	case '<', '\uFF1C', '\u02C2', '\uFE64', '\u3008', '\u2329', '\u276E',
		'\u276C', '\u27E8', '\u27EA', '\u300A', '\u22B2', '\u1438':
		return true
	}
	return false
}

// tagSkippable reports whether r renders as nothing or as spacing.
func tagSkippable(r rune) bool {
	if r < utf8.RuneSelf {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
	}
	return unicode.IsSpace(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) ||
		unicode.Is(unicode.Mc, r) || danger.ContainsInvisible(string(r))
}

// tagFold folds a rune to its lower-case ASCII look-alike where one exists.
func tagFold(r rune) rune {
	if r < utf8.RuneSelf {
		return unicode.ToLower(r)
	}
	if r >= 0xFF01 && r <= 0xFF5E {
		r -= 0xFEE0
	}
	if rep, ok := tagLookalikeFold[r]; ok {
		return rep
	}
	if rep, ok := tagEnclosedLetter(r); ok {
		return rep
	}
	if r >= utf8.RuneSelf {
		f, _ := utf8.DecodeRuneInString(danger.FoldHomoglyphs(string(r)))
		r = f
	}
	return unicode.ToLower(r)
}

// tagLookalikeFold folds the characters the shared homoglyph table does not
// cover that can stand in for the tag's letters (small capitals) or for its
// hyphen (dash and minus variants).
var tagLookalikeFold = map[rune]rune{
	'\u1D1C': 'u', '\u0274': 'n', '\u1D1B': 't', '\u0280': 'r', '\uA731': 's',
	'\u1D07': 'e', '\u1D05': 'd', '\u1D04': 'c', '\u1D0F': 'o',
	'\u2215': '/', '\u2044': '/', '\u29F8': '/', '\u2571': '/',
	'\u2010': '-', '\u2011': '-', '\u2012': '-', '\u2013': '-', '\u2014': '-',
	'\u2015': '-', '\u2212': '-', '\uFE58': '-',
	// Letters from other scripts and compatibility forms that render as
	// the tag's letters.
	'\u057D': 'u', '\u0578': 'n', '\u0585': 'o', '\u0501': 'd', '\u03F2': 'c',
	'\u217D': 'c', '\u217E': 'd', '\u017F': 's',
	'\u13A2': 't', '\u13CC': 'u', '\u13DA': 's', '\u13AC': 'e', '\u13D2': 'r',
	'\u13A0': 'd', '\u13DF': 'c', '\u13BE': 'o',
}

// tagEnclosedLetter folds squared, negative circled, negative squared and
// regional-indicator letters to their lower-case ASCII letter.
func tagEnclosedLetter(r rune) (rune, bool) {
	switch {
	case r >= 0x1F130 && r <= 0x1F149:
		return 'a' + (r - 0x1F130), true
	case r >= 0x1F150 && r <= 0x1F169:
		return 'a' + (r - 0x1F150), true
	case r >= 0x1F170 && r <= 0x1F189:
		return 'a' + (r - 0x1F170), true
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return 'a' + (r - 0x1F1E6), true
	}
	return 0, false
}

// tagWordRune reports whether a folded rune belongs to a word. Modifier
// letters (Lm), such as U+02CD, render as punctuation like an underscore and
// are separators, as is every other non-alphanumeric rune.
func tagWordRune(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	}
	return (unicode.IsLetter(r) && !unicode.Is(unicode.Lm, r)) || unicode.IsDigit(r)
}

// tagLookalikeAt reports whether rest (the text after an opener) begins with
// a look-alike of an untrusted-content tag name.
func tagLookalikeAt(rest string) bool {
	skips := 0
	// spaced records whitespace between the opener and the first letter of
	// the tag name; such a form is accepted only in the full tag shape (see
	// the tail check), so "a < untrusted content" stays prose.
	spaced, inName := false, false
	// next returns the next non-skippable rune, folded.
	next := func() (rune, bool) {
		for len(rest) > 0 {
			r, sz := utf8.DecodeRuneInString(rest)
			rest = rest[sz:]
			if !inName && unicode.IsSpace(r) {
				spaced = true
			}
			if tagSkippable(r) {
				skips++
				continue
			}
			return tagFold(r), true
		}
		return 0, false
	}
	word := func(w string) bool {
		for _, want := range w {
			r, ok := next()
			if !ok || r != want {
				return false
			}
		}
		return true
	}
	r, ok := next()
	if !ok {
		return false
	}
	if r == '/' {
		if r, ok = next(); !ok {
			return false
		}
	}
	// r is the first rune of "untrusted".
	if r != 'u' || !word("ntrusted") {
		return false
	}
	inName = true
	// Up to tagMaxSeparators separator runes (never an angle bracket or
	// alphanumeric); spacing between the words counts as a separator.
	skipsBefore := skips
	n := 0
	for {
		r, ok = next()
		if !ok {
			return false
		}
		if tagWordRune(r) || r == '<' || r == '>' {
			break
		}
		if n++; n > tagMaxSeparators {
			return false
		}
	}
	if (n == 0 && skips == skipsBefore) || r != 'c' || !word("ontent") {
		return false
	}
	// A real tag continues with its nonce ('_' / digits / hex), closes, or
	// ends; a longer hyphenated or lettered name is a different word.
	r, ok = next()
	if spaced {
		// Spacing after the opener is accepted only for the joined tag
		// shape: a real separator between the words (not just spacing) and
		// a nonce suffix, a close or the end after the name.
		return n > 0 && (!ok || r == '>' || r == '_' || unicode.IsDigit(r))
	}
	return !ok || (!tagWordRune(r) || unicode.IsDigit(r)) && r != '-'
}
