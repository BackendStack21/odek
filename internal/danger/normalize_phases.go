package danger

import (
	"strconv"
	"strings"
)

// ── Lexical context tracking ───────────────────────────────────────────
//
// Several normalisation phases rewrite the command text and must agree on
// which bytes are quoted, escaped, commented out or nested inside a command
// substitution. shellLex is the shared left-to-right tracker for that: a
// phase calls advance for every byte it does not handle itself and reads
// the quote flags to decide whether a construct is live.

// lexFrame saves the quote state of the context a nested construct (command
// substitution, subshell, backtick body, arithmetic) was opened in. Quoting
// restarts inside such a construct and is restored when it closes.
type lexFrame struct {
	double bool
	kind   byte // '$' for $( , '(' for a subshell, '`' for a backtick body, 'a' for arithmetic
	depth  int  // arithmetic only: open inner parentheses
}

type shellLex struct {
	single bool // inside '...' or $'...'
	ansi   bool // the single-quote span is an ANSI-C $'...' span (backslash escapes)
	double bool
	frames []lexFrame
}

// top reports whether the next byte is outside every quote and nested
// construct: the position where operators and word boundaries are live.
func (l *shellLex) top() bool {
	return !l.single && !l.double && len(l.frames) == 0
}

func (l *shellLex) push(kind byte) {
	l.frames = append(l.frames, lexFrame{double: l.double, kind: kind})
	l.double = false
}

func (l *shellLex) pop() {
	n := len(l.frames)
	l.double = l.frames[n-1].double
	l.frames = l.frames[:n-1]
}

func (l *shellLex) inArith() bool {
	n := len(l.frames)
	return n > 0 && l.frames[n-1].kind == 'a'
}

// commentLen returns the length of the comment starting at s[i] up to (not
// including) its newline, or 0 when s[i] does not start a comment: a `#`
// begins one only at the start of a word outside every quote.
func (l *shellLex) commentLen(s string, i int) int {
	if l.single || l.double || s[i] != '#' || (i > 0 && strings.IndexByte(" \t\n;&|(", s[i-1]) < 0) {
		return 0
	}
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return j
	}
	return len(s) - i
}

// advance updates the state for the syntax element at s[i] and returns how
// many bytes it spans (always at least 1): an escaped pair, a `$(` opener, a
// comment up to (not including) its newline, or a single byte.
func (l *shellLex) advance(s string, i int) int {
	c := s[i]
	if l.single {
		if l.ansi && c == '\\' && i+1 < len(s) {
			return 2
		}
		if c == '\'' {
			l.single, l.ansi = false, false
		}
		return 1
	}
	if c == '\\' && i+1 < len(s) {
		return 2
	}
	if l.inArith() {
		top := &l.frames[len(l.frames)-1]
		switch c {
		case '(':
			top.depth++
			return 1
		case ')':
			if top.depth > 0 {
				top.depth--
				return 1
			}
			l.pop()
			if i+1 < len(s) && s[i+1] == ')' {
				return 2
			}
			return 1
		}
	}
	switch c {
	case '#':
		if n := l.commentLen(s, i); n > 0 {
			return n
		}
	case '\'':
		if !l.double {
			l.single = true
		}
	case '"':
		l.double = !l.double
	case '$':
		if i+1 < len(s) {
			switch s[i+1] {
			case '\'':
				if !l.double {
					l.single, l.ansi = true, true
					return 2
				}
			case '(':
				if i+2 < len(s) && s[i+2] == '(' {
					l.push('a')
					return 3
				}
				l.push('$')
				return 2
			}
		}
	case '(':
		if !l.double {
			if i+1 < len(s) && s[i+1] == '(' {
				l.push('a')
				return 2
			}
			l.push('(')
		}
	case ')':
		if !l.double && len(l.frames) > 0 {
			l.pop()
		}
	case '`':
		if n := len(l.frames); n > 0 && l.frames[n-1].kind == '`' {
			l.pop()
		} else {
			l.push('`')
		}
	}
	return 1
}

// ── Line continuations ─────────────────────────────────────────────────

// joinLineContinuations deletes backslash-newline pairs, which the shell
// removes before tokenising. Inside single quotes and ANSI-C spans the pair
// is data, and inside a comment a backslash does not continue the comment,
// so those are kept. Running first keeps a continued command line from being
// split into two commands by the newline-to-separator rewrite.
func joinLineContinuations(cmd string) string {
	if !strings.Contains(cmd, "\\\n") {
		return cmd
	}
	var out strings.Builder
	var lex shellLex
	for i := 0; i < len(cmd); {
		if cmd[i] == '\\' && i+1 < len(cmd) && cmd[i+1] == '\n' && !lex.single {
			i += 2
			continue
		}
		n := lex.advance(cmd, i)
		out.WriteString(cmd[i : i+n])
		i += n
	}
	return out.String()
}

// stripComments removes shell comments. The shell discards them, but the
// later phases track quotes and would read an apostrophe in a comment as a
// quote opener that swallows the commands on the following lines.
func stripComments(cmd string) string {
	if !strings.Contains(cmd, "#") {
		return cmd
	}
	var out strings.Builder
	var lex shellLex
	for i := 0; i < len(cmd); {
		if n := lex.commentLen(cmd, i); n > 0 {
			i += n
			continue
		}
		n := lex.advance(cmd, i)
		out.WriteString(cmd[i : i+n])
		i += n
	}
	return out.String()
}

// ── Here-documents ─────────────────────────────────────────────────────

// heredocDataConsumers are programs that only read a here-document as data.
// For these the body is not shell text: classifying its lines as commands
// turned every prose or code heredoc into an unknown verb. Interpreters and
// anything else keep their body in the command text so it is classified.
var heredocDataConsumers = map[string]bool{
	"cat": true, "tee": true, "wc": true, "sort": true, "uniq": true,
	"head": true, "tail": true, "tr": true, "cut": true, "grep": true,
	"fgrep": true, "egrep": true, "nl": true, "fold": true, "rev": true,
	"tac": true, "base64": true, "sha256sum": true, "md5sum": true,
}

type pendingHeredoc struct {
	delim    string
	quoted   bool
	stripTab bool
}

// consumeHeredocs removes the bodies of here-documents fed to data-only
// programs, together with the operator and delimiter word. A body introduced
// by an unquoted delimiter still expands $(…) and `…`, so those spans are
// re-emitted on a following `echo` line where substitution extraction finds
// them. Any shape that cannot be resolved with certainty (missing terminator,
// unmatched substitution, pipe or process substitution on the operator line)
// leaves the whole command unchanged so the body keeps being classified.
func consumeHeredocs(cmd string) string {
	if !strings.Contains(cmd, "<<") {
		return cmd
	}
	var out strings.Builder
	var lex shellLex
	var pending []pendingHeredoc
	segStart := 0
	lineStart := 0
	for i := 0; i < len(cmd); {
		c := cmd[i]
		unquoted := !lex.single && !lex.double
		if unquoted && c == '\n' {
			out.WriteByte('\n')
			i++
			if len(pending) > 0 {
				spans, next, ok := readHeredocBodies(cmd, i, pending)
				if !ok {
					return cmd
				}
				pending = nil
				if len(spans) > 0 {
					out.WriteString("echo " + strings.Join(spans, " ") + "\n")
				}
				i = next
			}
			segStart, lineStart = i, i
			continue
		}
		if unquoted && c == '<' && i+1 < len(cmd) && cmd[i+1] == '<' && !lex.inArith() &&
			(i == 0 || cmd[i-1] != '<') && (i+2 >= len(cmd) || cmd[i+2] != '<') {
			h, end, ok := parseHeredocOperator(cmd, i)
			if ok && heredocRecipientIsData(cmd[segStart:i], cmd[lineStart:i], cmd[end:]) {
				pending = append(pending, h)
				built := out.String()
				trimmed := strings.TrimRight(built, "0123456789")
				if len(trimmed) < len(built) && (trimmed == "" || strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t")) {
					out.Reset()
					out.WriteString(trimmed)
				}
				i = end
				continue
			}
		}
		frames := len(lex.frames)
		n := lex.advance(cmd, i)
		if unquoted && n == 1 && strings.IndexByte(";|&", c) >= 0 || len(lex.frames) > frames {
			// A separator, or a nested construct ($(, (, `) opening a new
			// command, starts a new simple command.
			segStart = i + n
		}
		out.WriteString(cmd[i : i+n])
		i += n
	}
	if len(pending) > 0 {
		return cmd
	}
	return out.String()
}

// parseHeredocOperator parses `<<[-]WORD` at cmd[i:] and returns the
// delimiter and the index just past the delimiter word.
func parseHeredocOperator(cmd string, i int) (pendingHeredoc, int, bool) {
	var h pendingHeredoc
	j := i + 2
	if j < len(cmd) && cmd[j] == '-' {
		h.stripTab = true
		j++
	}
	for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	var word strings.Builder
	start := j
	for j < len(cmd) {
		c := cmd[j]
		switch {
		case c == '\'' || c == '"':
			k := strings.IndexByte(cmd[j+1:], c)
			if k < 0 {
				return h, 0, false
			}
			word.WriteString(cmd[j+1 : j+1+k])
			h.quoted = true
			j += k + 2
			continue
		case c == '\\' && j+1 < len(cmd) && cmd[j+1] != '\n':
			word.WriteByte(cmd[j+1])
			h.quoted = true
			j += 2
			continue
		case c == '$' && j+1 < len(cmd) && (cmd[j+1] == '\'' || cmd[j+1] == '(' || cmd[j+1] == '"'):
			return h, 0, false
		case c == '`':
			return h, 0, false
		case strings.IndexByte(" \t\n;|&<>()", c) >= 0:
		default:
			word.WriteByte(c)
			j++
			continue
		}
		break
	}
	if j == start || word.Len() == 0 {
		return h, 0, false
	}
	h.delim = word.String()
	return h, j, true
}

// heredocRecipientIsData reports whether the command owning the operator
// only consumes the body as data. segment is the text of the simple command
// before the operator, line the text of the whole line before it, and rest
// the remainder of the input after the delimiter word.
func heredocRecipientIsData(segment, line, rest string) bool {
	if strings.Contains(line, ">(") || strings.Contains(line, "<(") {
		return false
	}
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	if strings.ContainsAny(rest, "|(`") || strings.Contains(rest, "$(") {
		return false
	}
	words := tokenize(segment)
	k := 0
	for k < len(words) && isAssignment(words[k]) {
		k++
	}
	return k < len(words) && heredocDataConsumers[words[k]]
}

// readHeredocBodies consumes one body per pending operator starting at
// cmd[i:]. It returns the command/backtick substitution spans found in
// unquoted-delimiter bodies and the index just past the last terminator line.
func readHeredocBodies(cmd string, i int, pending []pendingHeredoc) ([]string, int, bool) {
	var spans []string
	for _, h := range pending {
		found := false
		bodyStart := i
		for i <= len(cmd) {
			end := strings.IndexByte(cmd[i:], '\n')
			line := ""
			next := len(cmd)
			if end >= 0 {
				line = cmd[i : i+end]
				next = i + end + 1
			} else {
				line = cmd[i:]
			}
			if h.stripTab {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				found = true
				if !h.quoted {
					s, ok := heredocSubstitutionSpans(cmd[bodyStart:i])
					if !ok {
						return nil, 0, false
					}
					spans = append(spans, s...)
				}
				i = next
				break
			}
			if end < 0 {
				break
			}
			i = next
		}
		if !found {
			return nil, 0, false
		}
	}
	return spans, i, true
}

// heredocSubstitutionSpans returns the $(…) and `…` spans of an unquoted
// here-document body, the only parts of it the shell executes.
func heredocSubstitutionSpans(body string) ([]string, bool) {
	var spans []string
	for i := 0; i < len(body); {
		switch {
		case body[i] == '\\' && i+1 < len(body):
			i += 2
		case body[i] == '$' && i+1 < len(body) && body[i+1] == '(':
			depth, j := 1, i+2
			for j < len(body) && depth > 0 {
				switch body[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			if depth != 0 {
				return nil, false
			}
			spans = append(spans, body[i:j])
			i = j
		case body[i] == '`':
			j := i + 1
			for j < len(body) && body[j] != '`' {
				if body[j] == '\\' && j+1 < len(body) {
					j++
				}
				j++
			}
			if j >= len(body) {
				return nil, false
			}
			spans = append(spans, body[i:j+1])
			i = j + 1
		default:
			i++
		}
	}
	return spans, true
}

// ── ANSI-C quoting ─────────────────────────────────────────────────────

// decodeANSIC rewrites $'...' ANSI-C quoted strings to their literal value,
// so `$'\x72\x6d' -rf /` and `$'\162m'` reduce to `rm`. Without this an
// attacker hides a command name in hex/octal escapes the tokenizer can't see.
//
// A `$'` is an opener only outside single quotes, double quotes and
// comments, so `'$'` is left alone. A decoded value containing a quote,
// space, `;`, newline or other syntax is emitted as a single-quoted literal:
// in the shell it is data and must not become syntax in the rewritten text.
func decodeANSIC(cmd string) string {
	if !strings.Contains(cmd, "$'") {
		return cmd
	}
	var out strings.Builder
	var lex shellLex
	for i := 0; i < len(cmd); {
		if !lex.single && !lex.double && cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '\'' {
			if word, end, ok := decodeANSICWord(cmd, i+2); ok {
				out.WriteString(quoteDecodedWord(word))
				i = end
				continue
			}
		}
		n := lex.advance(cmd, i)
		out.WriteString(cmd[i : i+n])
		i += n
	}
	return out.String()
}

// quoteDecodedWord renders a decoded $'…' value so later phases see it as
// one literal word. Plain words (letters, digits and path/option punctuation)
// are emitted bare; anything containing whitespace, a quote or other shell
// syntax is single-quoted, with an embedded quote written as an escaped pair.
func quoteDecodedWord(word string) string {
	plain := true
	for i := 0; i < len(word); i++ {
		c := word[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_-./:=@%+", c) >= 0) {
			plain = false
			break
		}
	}
	if plain {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// decodeANSICWord decodes the body of a $'…' string starting at cmd[j:] and
// returns the value and the index just past the closing quote.
func decodeANSICWord(cmd string, j int) (string, int, bool) {
	var body strings.Builder
	for j < len(cmd) && cmd[j] != '\'' {
		if cmd[j] == '\\' && j+1 < len(cmd) {
			j += decodeEscape(cmd[j:], &body)
			continue
		}
		body.WriteByte(cmd[j])
		j++
	}
	if j >= len(cmd) {
		return "", 0, false
	}
	return body.String(), j + 1, true
}

// decodeEscape decodes one backslash escape at the start of s into b and
// returns how many bytes of s were consumed.
func decodeEscape(s string, b *strings.Builder) int {
	if len(s) < 2 {
		b.WriteByte('\\')
		return 1
	}
	switch s[1] {
	case 'n':
		b.WriteByte('\n')
		return 2
	case 't':
		b.WriteByte('\t')
		return 2
	case 'r':
		b.WriteByte('\r')
		return 2
	case 'a':
		b.WriteByte(7)
		return 2
	case 'b':
		b.WriteByte(8)
		return 2
	case 'e', 'E':
		b.WriteByte(27)
		return 2
	case 'f':
		b.WriteByte(12)
		return 2
	case 'v':
		b.WriteByte(11)
		return 2
	case '\\', '\'', '"':
		b.WriteByte(s[1])
		return 2
	case 'c': // \cX control character
		if len(s) >= 3 {
			if s[2] == '?' {
				b.WriteByte(0x7f)
			} else {
				b.WriteByte(s[2] & 0x1f)
			}
			return 3
		}
	case 'x': // \xH or \xHH
		if end := hexRun(s, 2, 2); end > 2 {
			v, _ := strconv.ParseUint(s[2:end], 16, 8)
			b.WriteByte(byte(v))
			return end
		}
	case 'u', 'U': // \uHHHH / \UHHHHHHHH
		limit := 4
		if s[1] == 'U' {
			limit = 8
		}
		if end := hexRun(s, 2, limit); end > 2 {
			if v, err := strconv.ParseUint(s[2:end], 16, 32); err == nil && v <= 0x10FFFF {
				b.WriteRune(rune(v))
				return end
			}
		}
	default:
		if s[1] >= '0' && s[1] <= '7' { // \NNN octal (1–3 digits, like bash)
			// end starts after the backslash+first digit; cap at end<4 so at
			// most 3 octal digits (s[1:4]) are consumed. A wider bound would
			// swallow a following literal octal digit and diverge from the
			// shell (bash: $'\1551' → "m1", not one byte).
			end := 2
			for end < len(s) && end < 4 && s[end] >= '0' && s[end] <= '7' {
				end++
			}
			if v, err := strconv.ParseUint(s[1:end], 8, 8); err == nil {
				b.WriteByte(byte(v)) // bash takes octal escapes mod 256
				return end
			}
		}
	}
	b.WriteByte(s[1])
	return 2
}

// hexRun returns the index just past up to limit hex digits of s starting at
// from (from itself when there are none).
func hexRun(s string, from, limit int) int {
	end := from
	for end < len(s) && end-from < limit && isHexDigit(s[end]) {
		end++
	}
	return end
}

// ── Brace expansion ────────────────────────────────────────────────────

const (
	maxBraceWords     = 1024     // alternatives produced for a single word
	maxBraceWordBytes = 64 << 10 // bytes produced for a single word
	maxBraceWork      = 4 << 20  // bytes scanned across one command

	// braceOverflowToken is emitted as its own (unknown) command when a word
	// expands past the caps, so an expansion too large to inspect is denied
	// instead of passing through unexpanded.
	braceOverflowToken = "odek.brace-overflow"
)

type braceBudget struct{ work int }

func isBraceBoundary(c byte) bool {
	return strings.IndexByte(" \t\n\r;|&<>()", c) >= 0
}

// expandBraces approximates brace expansion for the classifier. Each word
// is expanded as the shell would: the text glued before and after a
// {a,b} group is distributed over the alternatives and nested groups are
// expanded to a fixed point, so `/et{c,c}/shadow` is seen as
// `/etc/shadow /etc/shadow` and `{rm,-rf,/}` as `rm -rf /`. Quoted braces,
// ${…} parameter expansions, find's {} and groups without a comma are left
// alone. Output size is capped; see braceOverflowToken.
func expandBraces(cmd string) string {
	if !strings.Contains(cmd, "{") || !strings.Contains(cmd, ",") {
		return cmd
	}
	var out strings.Builder
	budget := &braceBudget{}
	for i := 0; i < len(cmd); {
		c := cmd[i]
		if isBraceBoundary(c) {
			out.WriteByte(c)
			i++
			continue
		}
		if c == '#' && (i == 0 || isBraceBoundary(cmd[i-1])) {
			j := strings.IndexByte(cmd[i:], '\n')
			if j < 0 {
				j = len(cmd) - i
			}
			out.WriteString(cmd[i : i+j])
			i += j
			continue
		}
		var lex shellLex
		end := i
		for end < len(cmd) && !(lex.top() && isBraceBoundary(cmd[end])) {
			end += lex.advance(cmd, end)
		}
		word := cmd[i:end]
		i = end
		if !strings.Contains(word, "{") {
			out.WriteString(word)
			continue
		}
		words, ok := braceExpandWord(word, budget)
		if !ok {
			out.WriteString(" ; " + braceOverflowToken + " ; " + word)
			continue
		}
		if len(words) == 1 && words[0] == word {
			out.WriteString(word)
			continue
		}
		out.WriteString(" ")
		first := true
		for _, w := range words {
			if w == "" {
				continue
			}
			if !first {
				out.WriteByte(' ')
			}
			out.WriteString(w)
			first = false
		}
		out.WriteString(" ")
	}
	return out.String()
}

// braceExpandWord expands every brace group in w. ok is false when the
// expansion exceeds the caps.
func braceExpandWord(w string, b *braceBudget) ([]string, bool) {
	b.work += len(w)
	if b.work > maxBraceWork {
		return nil, false
	}
	start, end, commas := firstBraceGroup(w, b)
	if b.work > maxBraceWork {
		return nil, false
	}
	if start < 0 {
		return []string{w}, true
	}
	pre, post := w[:start], w[end+1:]
	postWords, ok := braceExpandWord(post, b)
	if !ok {
		return nil, false
	}
	var alts []string
	prev := start + 1
	for _, c := range commas {
		alts = append(alts, w[prev:c])
		prev = c + 1
	}
	alts = append(alts, w[prev:end])
	var res []string
	size := 0
	for _, alt := range alts {
		altWords, ok := braceExpandWord(alt, b)
		if !ok {
			return nil, false
		}
		for _, a := range altWords {
			for _, p := range postWords {
				s := pre + a + p
				size += len(s)
				if len(res) >= maxBraceWords || size > maxBraceWordBytes {
					return nil, false
				}
				res = append(res, s)
			}
		}
	}
	return res, true
}

// firstBraceGroup finds the leftmost {…} group in w that the shell would
// expand: an unquoted brace not introducing ${…} whose matching close has
// at least one top-level comma. It returns the index of the open brace, the
// index of its close and the indexes of the top-level commas, or -1.
func firstBraceGroup(w string, b *braceBudget) (int, int, []int) {
	var lex shellLex
	for i := 0; i < len(w); {
		if w[i] == '{' && lex.top() && (i == 0 || w[i-1] != '$') {
			end, commas := matchBrace(w, i)
			if end >= 0 && len(commas) > 0 {
				return i, end, commas
			}
			// Each failed match rescans the rest of the word; charge it so
			// a word of unclosed braces cannot cost quadratic time.
			if b.work += len(w) - i; b.work > maxBraceWork {
				return -1, -1, nil
			}
		}
		i += lex.advance(w, i)
	}
	return -1, -1, nil
}

// matchBrace returns the close of the group opened at w[open] and its
// top-level commas, or -1 when the group is never closed.
func matchBrace(w string, open int) (int, []int) {
	var lex shellLex
	var commas []int
	depth := 1
	for i := open + 1; i < len(w); {
		if lex.top() {
			switch w[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return i, commas
				}
			case ',':
				if depth == 1 {
					commas = append(commas, i)
				}
			}
		}
		i += lex.advance(w, i)
	}
	return -1, nil
}
