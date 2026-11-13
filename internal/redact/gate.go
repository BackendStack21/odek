package redact

import (
	"regexp"
	"strings"
)

// ── Pattern gates ──────────────────────────────────────────────────────
//
// Most patterns cannot use the regexp engine's literal-prefix skip: they begin
// with \b, (?i), an alternation or a character class, so on their own they run
// a full automaton pass over every text, secret or not. A gate is a cheap
// necessary condition for a pattern to match, derived from the pattern text:
// literals that every match contains, or a byte shape that every match has.
// When the text does not meet the gate the pattern cannot match and is not
// run. A gate never changes a result; it only skips a pattern that could not
// have matched. A pattern without a gate always runs (those with a literal
// prefix already make the engine skip to the next occurrence of it).

// gateLit is one literal of a gate group.
type gateLit struct {
	s      string // lower-case ASCII when fold is set
	fold   bool   // matched in any letter case, as the pattern's (?i) does
	anchor int    // index in s of its rarest byte, which the search jumps between
}

// patternGate is a necessary condition for a match: the text holds a literal
// of every group; when keys is set, one of the keys, in some letter case, is
// directly followed by a key separator (see keyBeforeSeparator); and shape,
// when set, reports true for it.
type patternGate struct {
	groups [][]gateLit
	keys   []gateLit
	shape  func(text string) bool
}

// patternGates holds the gate of each gated pattern. It is written only while
// the package variables are initialised.
var patternGates = map[*regexp.Regexp]patternGate{}

// gated records g as the gate of re and returns re.
func gated(re *regexp.Regexp, g patternGate) *regexp.Regexp {
	patternGates[re] = g
	return re
}

// literals is a gate made of literal groups.
func literals(groups ...[]gateLit) patternGate {
	return patternGate{groups: groups}
}

// shaped is a gate given by a byte shape every match has.
func shaped(shape func(text string) bool) patternGate {
	return patternGate{shape: shape}
}

// keyed is the gate of a key pattern followed by gSep: every match holds one
// of keys, in some letter case, directly followed by the separator.
func keyed(keys ...string) patternGate {
	return patternGate{keys: folded(keys...)}
}

// exact is a group of literals matched as they are written.
func exact(lits ...string) []gateLit {
	out := make([]gateLit, len(lits))
	for i, l := range lits {
		out[i] = gateLit{s: l, anchor: rarestByte(l)}
	}
	return out
}

// folded is a group of literals matched in any letter case.
func folded(lits ...string) []gateLit {
	out := make([]gateLit, len(lits))
	for i, l := range lits {
		l = strings.ToLower(l)
		out[i] = gateLit{s: l, fold: true, anchor: rarestByte(l)}
	}
	return out
}

// letterFrequency lists the lower-case letters from the commonest to the
// rarest in English text.
const letterFrequency = "etaoinsrhdlcumwfgypbvkjxqz"

// byteCommonness estimates how often c occurs in prose, code and logs (higher
// is commoner). It only steers which byte of a literal a search jumps between,
// never whether the literal is found.
func byteCommonness(c byte) int {
	switch {
	case c >= 'a' && c <= 'z':
		return 100 - strings.IndexByte(letterFrequency, c)
	case c >= 'A' && c <= 'Z':
		return 80 // capitals: as rare as v
	default:
		return 88 // digits and punctuation: as common as u
	}
}

// rarestByte returns the index of the least common byte of lit.
func rarestByte(lit string) int {
	best := 0
	for i := 1; i < len(lit); i++ {
		if byteCommonness(lit[i]) < byteCommonness(lit[best]) {
			best = i
		}
	}
	return best
}

// indexAnchored returns the index of the first occurrence of lit in text at
// or after from, or -1. It jumps between the occurrences of lit[a] and
// compares lit around each, so a literal whose first byte is common (t, s, a)
// costs as little as one whose first byte is rare.
func indexAnchored(text, lit string, a, from int) int {
	c := lit[a]
	last := len(text) - len(lit) + a // last offset lit[a] can sit at
	for i := from + a; i <= last; i++ {
		j := strings.IndexByte(text[i:last+1], c)
		if j < 0 {
			return -1
		}
		i += j
		if text[i-a:i-a+len(lit)] == lit {
			return i - a
		}
	}
	return -1
}

// containsAnchored reports whether text contains lit (see indexAnchored).
func containsAnchored(text, lit string, a int) bool {
	return indexAnchored(text, lit, a, 0) >= 0
}

// separatorAt reports whether text[i:] begins the way every match of gSep
// does: an optional backslash and quote, blanks (\s), then '=' (of = or =>)
// or ':'.
func separatorAt(text string, i int) bool {
	if i < len(text) && text[i] == '\\' {
		i++
	}
	if i < len(text) && (text[i] == '"' || text[i] == '\'' || text[i] == '`') {
		i++
	}
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\f' || text[i] == '\r') {
		i++
	}
	return i < len(text) && (text[i] == '=' || text[i] == ':')
}

// textScan is the text a redaction stage runs on, with the facts the gates
// consult, each computed at most once for that text.
type textScan struct {
	text    string
	ungated bool // run every pattern: the behaviour the gates must reproduce

	lowered   bool
	lower     string // text with the ASCII letters A-Z lower-cased
	foldKnown bool
	foldRunes bool // text holds U+212A KELVIN SIGN or U+017F LATIN SMALL LETTER LONG S
}

func newScan(text string, ungated bool) *textScan {
	return &textScan{text: text, ungated: ungated}
}

// set replaces the text after a stage rewrote it; the facts about the new
// text are computed again when a gate needs them.
func (s *textScan) set(text string) {
	*s = textScan{text: text, ungated: s.ungated}
}

// may reports whether re can match the text: it has no gate, gates are off,
// or the text meets its gate.
func (s *textScan) may(re *regexp.Regexp) bool {
	if s.ungated {
		return true
	}
	g, ok := patternGates[re]
	if !ok {
		return true
	}
	for _, group := range g.groups {
		if !s.holdsAny(group) {
			return false
		}
	}
	if g.keys != nil && !s.keyBeforeSeparator(g.keys) {
		return false
	}
	return g.shape == nil || g.shape(s.text)
}

// keyBeforeSeparator reports whether one of keys (lower-case ASCII) occurs in
// the text, in any letter case, directly followed by a key separator (see
// separatorAt). A key with a k or an s counts as followed by one whenever the
// text holds a rune that folds to k or s (see containsFold).
func (s *textScan) keyBeforeSeparator(keys []gateLit) bool {
	lower := s.lowerText()
	for _, k := range keys {
		if strings.ContainsAny(k.s, "ks") && s.hasFoldRunes() {
			return true
		}
		for from := 0; ; {
			i := indexAnchored(lower, k.s, k.anchor, from)
			if i < 0 {
				break
			}
			if separatorAt(lower, i+len(k.s)) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

// holdsAny reports whether the text holds a literal of group.
func (s *textScan) holdsAny(group []gateLit) bool {
	for _, l := range group {
		if l.fold {
			if s.containsFold(l) {
				return true
			}
		} else if containsAnchored(s.text, l.s, l.anchor) {
			return true
		}
	}
	return false
}

// containsFold reports whether a (?i) pattern can find l.s (lower-case ASCII)
// in the text. Unicode case folding pairs k with U+212A KELVIN SIGN and s with
// U+017F LATIN SMALL LETTER LONG S, the only non-ASCII runes that fold to an
// ASCII letter, so a literal with a k or an s may also be spelled with them:
// when the text holds either rune such a literal is taken as present.
func (s *textScan) containsFold(l gateLit) bool {
	if containsAnchored(s.lowerText(), l.s, l.anchor) {
		return true
	}
	return strings.ContainsAny(l.s, "ks") && s.hasFoldRunes()
}

// indexFoldFrom returns the offset of the first occurrence of word (lower-case
// ASCII) in the text at or after pos, in any ASCII letter case, or -1: the
// result of indexFoldASCII(text[pos:], word) shifted by pos. It searches the
// lower-cased copy, so a sequence of calls with increasing pos scans the text
// once; indexFoldASCII rescans to the end for a letter case the rest of the
// text lacks on every call, which is quadratic over many occurrences. The
// ungated path keeps indexFoldASCII as the reference.
func (s *textScan) indexFoldFrom(word string, pos int) int {
	var j int
	if s.ungated {
		j = indexFoldASCII(s.text[pos:], word)
	} else {
		j = strings.Index(s.lowerText()[pos:], word)
	}
	if j < 0 {
		return -1
	}
	return pos + j
}

func (s *textScan) lowerText() string {
	if !s.lowered {
		s.lower, s.lowered = lowerASCII(s.text), true
	}
	return s.lower
}

func (s *textScan) hasFoldRunes() bool {
	if !s.foldKnown {
		s.foldRunes = strings.Contains(s.text, "\u212a") || strings.Contains(s.text, "\u017f")
		s.foldKnown = true
	}
	return s.foldRunes
}

// lowerASCII returns text with the ASCII letters A-Z lower-cased and every
// other byte unchanged, so offsets and multi-byte runes are preserved.
func lowerASCII(text string) string {
	i := 0
	for i < len(text) && (text[i] < 'A' || text[i] > 'Z') {
		i++
	}
	if i == len(text) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	pos := 0
	for ; i < len(text); i++ {
		if c := text[i]; c >= 'A' && c <= 'Z' {
			b.WriteString(text[pos:i])
			b.WriteByte(c + ('a' - 'A'))
			pos = i + 1
		}
	}
	b.WriteString(text[pos:])
	return b.String()
}

// isTokenByte reports whether c is in [A-Za-z0-9_-].
func isTokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// tokenBytesBefore reports whether the n bytes before text[i] are token bytes.
func tokenBytesBefore(text string, i, n int) bool {
	if i < n {
		return false
	}
	for k := i - n; k < i; k++ {
		if !isTokenByte(text[k]) {
			return false
		}
	}
	return true
}

// tokenBytesFrom reports whether the n bytes from text[i] are token bytes.
func tokenBytesFrom(text string, i, n int) bool {
	if len(text)-i < n {
		return false
	}
	for k := i; k < i+n; k++ {
		if !isTokenByte(text[k]) {
			return false
		}
	}
	return true
}

// digitColonShape reports whether some ':' in text has five ASCII digits just
// before it and 30 token bytes just after it. Every match of
// `\b[0-9]{5,}:[A-Za-z0-9_-]{30,}\b` has that shape.
func digitColonShape(text string) bool {
	for i := 0; i < len(text); i++ {
		j := strings.IndexByte(text[i:], ':')
		if j < 0 {
			return false
		}
		i += j
		if i >= 5 && allASCIIDigits(text[i-5:i]) && tokenBytesFrom(text, i+1, 30) {
			return true
		}
	}
	return false
}

// dottedTokenShape reports whether some '.' in text has 23 token bytes just
// before it, a run of 5 to 7 token bytes just after it that ends at a second
// '.', and 27 token bytes after that one. Every match of
// `\b[MNO][A-Za-z0-9_-]{22,27}\.[A-Za-z0-9_-]{5,7}\.[A-Za-z0-9_-]{27,40}\b`
// has that shape: [MNO] and the 22 to 27 bytes after it are token bytes that
// end at its first '.', and '.' is not a token byte, so the run after that
// '.' is exactly the second segment.
func dottedTokenShape(text string) bool {
	for i := 0; i < len(text); i++ {
		j := strings.IndexByte(text[i:], '.')
		if j < 0 {
			return false
		}
		i += j
		if !tokenBytesBefore(text, i, 23) {
			continue
		}
		run := 0
		for run < 8 && i+1+run < len(text) && isTokenByte(text[i+1+run]) {
			run++
		}
		if run >= 5 && run <= 7 && i+1+run < len(text) && text[i+1+run] == '.' && tokenBytesFrom(text, i+2+run, 27) {
			return true
		}
	}
	return false
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
