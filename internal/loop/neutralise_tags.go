package loop

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

const (
	// tagSkipBudget bounds the invisible/whitespace/combining runes skipped
	// while matching one candidate, so padding cannot hide a tag but cannot
	// make a candidate expensive either.
	tagSkipBudget = 64
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
// The scan is linear: a candidate is abandoned at its first mismatching rune.
func NeutraliseUntrustedLiterals(s string) string {
	s = strings.ReplaceAll(s, "untrusted_content", "untrusted·content")
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if (r == '<' || r == '＜') && tagLookalikeAt(s[i+sz:]) {
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
	if r >= utf8.RuneSelf {
		f, _ := utf8.DecodeRuneInString(danger.FoldHomoglyphs(string(r)))
		r = f
	}
	return unicode.ToLower(r)
}

// tagLookalikeAt reports whether rest (the text after an opener) begins with
// a look-alike of an untrusted-content tag name.
func tagLookalikeAt(rest string) bool {
	skips := 0
	// next returns the next non-skippable rune, folded.
	next := func() (rune, bool) {
		for len(rest) > 0 {
			r, sz := utf8.DecodeRuneInString(rest)
			rest = rest[sz:]
			if tagSkippable(r) {
				if skips++; skips > tagSkipBudget {
					return 0, false
				}
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
	// Up to tagMaxSeparators separator runes (never an angle bracket or
	// alphanumeric); spacing between the words counts as a separator.
	skipsBefore := skips
	n := 0
	for {
		r, ok = next()
		if !ok {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '<' || r == '>' {
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
	return !ok || !unicode.IsLetter(r) && r != '-'
}
