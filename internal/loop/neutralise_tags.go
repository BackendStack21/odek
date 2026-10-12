package loop

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

// tagLookalikeRe matches the folded start of an untrusted-content tag
// (opening or closing) after the '<': optional '/', the name with at most
// two arbitrary non-alphanumeric separator characters between its words.
var tagLookalikeRe = regexp.MustCompile(`^/?untrusted[^a-z0-9]{0,2}content`)

// tagLookalikeWindow bounds how many runes after a '<' are examined.
const tagLookalikeWindow = 48

// NeutraliseUntrustedLiterals makes a body unable to contain anything that
// reads as an untrusted-content boundary tag. The exact spelling
// "untrusted_content" is rewritten to "untrusted·content"; in addition every
// '<' that begins a look-alike of the tag name (upper case, Cyrillic or
// Greek confusables, fullwidth forms, invisible characters or a different
// separator) becomes '‹', so a forged close tag cannot read as the real
// boundary closing.
func NeutraliseUntrustedLiterals(s string) string {
	s = strings.ReplaceAll(s, "untrusted_content", "untrusted·content")
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		if tagLookalikeAt(s[i+1:]) {
			b.WriteString(s[last:i])
			b.WriteString("‹")
			last = i + 1
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// tagLookalikeAt reports whether rest (the text after a '<') begins with a
// look-alike of an untrusted-content tag name.
func tagLookalikeAt(rest string) bool {
	end, n := 0, 0
	for end < len(rest) && n < tagLookalikeWindow {
		_, sz := utf8.DecodeRuneInString(rest[end:])
		end += sz
		n++
	}
	folded := danger.FoldHomoglyphs(foldFullwidth(danger.NormalizeForScan(rest[:end])))
	return tagLookalikeRe.MatchString(strings.ToLower(folded))
}

// foldFullwidth maps the fullwidth ASCII block (U+FF01-U+FF5E) to ASCII.
func foldFullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0xFF01 && r <= 0xFF5E {
			return r - 0xFEE0
		}
		return r
	}, s)
}
