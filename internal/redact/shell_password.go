package redact

import (
	"regexp"
	"strings"
)

// Passwords given to the MySQL/MariaDB clients (-pVALUE, --password=VALUE),
// to sshpass (-p VALUE, -pVALUE) and in a MYSQL_PWD or SSHPASS assignment.
// The value is always one whole shell word, judged as one: it is a password
// unless it is wholly a reference, a command substitution, a placeholder or
// empty, so a reference glued to literal text is a literal. A password word
// is replaced up to its quotes: every stretch between them is replaced, and
// the quotes stay, so the text around it reads the same quoting afterwards
// and a second pass changes nothing.

// pwWordMax bounds the raw length of one password word, and so every search
// for a closing quote, brace or parenthesis inside it.
const pwWordMax = 4 << 10

// Segment kinds of a shell word.
const (
	segText   = iota // unquoted bytes, '$' included
	segSingle        // '...'
	segDouble        // "...", or \"...\" in JSON-encoded text
	segQuoted        // $'...' or $"..."
	segSubst         // $(...) without nested parentheses or quotes
	segBrace         // ${...} without nested braces or quotes
	segEscape        // a backslash and the unit it escapes
)

// pwSeg is one segment of a shell word: raw extent [s,e) and, for the quoted
// kinds, the raw extent [cs,ce) of the content between the quotes.
type pwSeg struct {
	kind         int
	s, e, cs, ce int
}

// quoteState is where a word starts: inside a JSON string (or a shell
// double-quoted string, which reads alike), or inside a shell single-quoted
// string that opened before it, as jsonStringTracker reads the text.
type quoteState struct{ json, single bool }

// trackQuotes returns the quote state at text[off]; t must be queried at
// increasing offsets.
func trackQuotes(t *jsonStringTracker, text string, off int) quoteState {
	json := t.at(text, off)
	return quoteState{json: json, single: !json && t.single}
}

// pwWord is one lexed shell word. A quote with no closing quote within the
// line (or the JSON string, or pwWordMax), the quote that closes a
// single-quoted string the word started in, an unmatched ')' and a backquote
// close something outside the word: they are kept as they are and split the
// word into runs, which are judged together.
type pwWord struct {
	text string
	e    int // raw end of the word
	runs [][]pwSeg
}

// pwLexer lexes one shell word. In JSON-encoded text (json) the word's shell
// quotes are escaped (\"), a \n, \r or \t escape is a blank and an unescaped
// double quote closes the JSON string, so it ends the word and every search.
// Inside an outer single-quoted string (outer), the next single quote closes
// it: no quote or substitution in the word reaches past it. In a list (list),
// a quoted string that a ',', ']', ')', '}' or ':' follows is the whole item.
//
// The quote state comes from counting the quotes before the word, and one
// stray quote earlier on the line inverts it. A double quote that is not
// followed the way a closed string is (see closesString) is therefore read
// as the shell's, and the word as plain text from there on, so a quoted
// password is never read as an empty word.
type pwLexer struct {
	text  string
	json  bool
	outer bool
	list  bool
	limit int
}

// closesString reports whether a double quote right before text[k] can close
// a JSON or source-code string: the text ends there, or a blank, a line break
// or one of , } ] : ) ; + . follows.
func closesString(text string, k int) bool {
	return k >= len(text) || strings.IndexByte(" \t\n\r,}]:);+.", text[k]) >= 0
}

// lexPasswordWord lexes the shell word that starts at text[vs]. A blank or a
// line break ends it; a backslash-newline continues it.
func lexPasswordWord(text string, vs int, q quoteState) pwWord {
	return lexPasswordItem(text, vs, q, false)
}

// lexPasswordItem is lexPasswordWord; list marks a word that may be an item
// of an argument list or the value of a mapping.
func lexPasswordItem(text string, vs int, q quoteState, list bool) pwWord {
	l := pwLexer{text: text, json: q.json, outer: q.single && !q.json, list: list, limit: len(text)}
	if l.limit-vs > pwWordMax {
		l.limit = vs + pwWordMax
	}
	w := pwWord{text: text}
	var run []pwSeg
	add := func(kind, s, e, cs, ce int) {
		if kind == segText && len(run) > 0 && run[len(run)-1].kind == segText && run[len(run)-1].e == s {
			run[len(run)-1].e = e
			return
		}
		run = append(run, pwSeg{kind, s, e, cs, ce})
	}
	keep := func() { // a kept separator ends the current run
		w.runs = append(w.runs, run)
		run = nil
	}
	i := vs
scan:
	for i < l.limit {
		c := text[i]
		switch {
		case c == '\n' || c == '\r' || c == ' ' || c == '\t' || (l.json && c == '"' && closesString(text, i+1)):
			break scan
		case c == '\\' && i+1 < len(text):
			n := text[i+1]
			switch {
			case n == '\n' || n == '\r':
				i += 2 // a line continuation: the word goes on
				if n == '\r' && i < len(text) && text[i] == '\n' {
					i++
				}
			case l.json && (n == 'n' || n == 'r' || n == 't'):
				break scan
			case l.json && n == '"':
				if k := l.scanDouble(i + 2); k >= 0 {
					add(segDouble, i, k+2, i+2, k)
					i = k + 2
				} else {
					keep()
					i += 2
				}
			default:
				// A backslash escapes the next unit; in JSON-encoded text an
				// escaped backslash is the shell's backslash.
				e := i + l.unitLen(i)
				add(segEscape, i, e, i, e)
				i = e
			}
		case c == '\'' && l.outer:
			l.outer = false
			keep()
			i++
		case c == '\'':
			if k := l.scanQuote(i+1, '\''); k >= 0 {
				add(segSingle, i, k+1, i+1, k)
				i = k + 1
			} else {
				keep()
				i++
			}
		case c == '"':
			// Plain text, or a quote that does not close a JSON string: the
			// quote state was wrong, and the word is plain text from here.
			l.json = false
			if k := l.scanDouble(i + 1); k >= 0 {
				add(segDouble, i, k+1, i+1, k)
				i = k + 1
			} else {
				keep()
				i++
			}
		case c == ')' || c == '`':
			keep()
			i++
		case (c == ';' || c == '|' || c == '&') && afterQuote(text, run, i):
			// The quote delimited the value; elsewhere these characters are
			// taken as part of it.
			break scan
		case l.list && (c == ',' || c == ']' || c == ')' || c == '}' || c == ':') && afterQuote(text, run, i):
			break scan
		case c == '$' && i+1 < len(text):
			if g, ok := l.dollar(i); ok {
				add(g.kind, g.s, g.e, g.cs, g.ce)
				i = g.e
			} else {
				add(segText, i, i+1, i, i+1)
				i++
			}
		default:
			add(segText, i, i+1, i, i+1)
			i++
		}
	}
	w.e = i
	w.runs = append(w.runs, run)
	return w
}

// afterQuote reports whether the run's last segment is a quoted string that
// closes right before text[i]. The redaction marker counts as one: it may
// stand where a quoted value was replaced, and a second pass must stop where
// the first did.
func afterQuote(text string, run []pwSeg, i int) bool {
	if len(run) == 0 || run[len(run)-1].e != i {
		return false
	}
	switch run[len(run)-1].kind {
	case segSingle, segDouble, segQuoted:
		return true
	case segText:
		return strings.HasSuffix(text[run[len(run)-1].s:i], "[REDACTED]")
	}
	return false
}

// dollar lexes the '$' at text[i] when it opens ANSI-C or locale quoting, a
// command substitution or a parameter expansion.
func (l *pwLexer) dollar(i int) (pwSeg, bool) {
	text := l.text
	switch n := text[i+1]; {
	case n == '\'' && !l.outer:
		if k := l.scanQuote(i+2, '\''); k >= 0 {
			return pwSeg{segQuoted, i, k + 1, i + 2, k}, true
		}
	case n == '"' && !l.json:
		if k := l.scanDouble(i + 2); k >= 0 {
			return pwSeg{segQuoted, i, k + 1, i + 2, k}, true
		}
	case n == '\\' && l.json && i+2 < len(text) && text[i+2] == '"':
		if k := l.scanDouble(i + 3); k >= 0 {
			return pwSeg{segQuoted, i, k + 2, i + 3, k}, true
		}
	case n == '(':
		if k := l.scanUntil(i+2, ')', '('); k >= 0 {
			return pwSeg{segSubst, i, k + 1, i, k + 1}, true
		}
	case n == '{':
		if k := l.scanUntil(i+2, '}', '{'); k >= 0 {
			return pwSeg{segBrace, i, k + 1, i, k + 1}, true
		}
	}
	return pwSeg{}, false
}

// unitLen is the raw length of the unit at text[j]: a backslash and the unit
// it escapes (in JSON-encoded text an escaped backslash and the unit after
// it), or one byte.
func (l *pwLexer) unitLen(j int) int {
	text := l.text
	n := 1
	if text[j] == '\\' && j+1 < len(text) {
		n = 2
		if l.json && text[j+1] == '\\' && j+2 < len(text) {
			n++
			if text[j+2] == '\\' && j+3 < len(text) {
				n++
			}
		}
	}
	return n
}

// stops reports whether the unit at text[j] ends every search: a line break,
// in JSON-encoded text the unescaped quote that closes the JSON string, and
// inside an outer single-quoted string the quote that closes it.
func (l *pwLexer) stops(j int) bool {
	c := l.text[j]
	return c == '\n' || c == '\r' || (l.json && c == '"') || (l.outer && c == '\'')
}

// scanQuote returns the offset of the unit q that closes a quote whose
// content starts at from, or -1.
func (l *pwLexer) scanQuote(from int, q byte) int {
	for j := from; j < l.limit; j += l.unitLen(j) {
		if l.stops(j) {
			return -1
		}
		if l.text[j] == q {
			return j
		}
	}
	return -1
}

// scanDouble returns the offset of the double quote that closes a
// double-quoted string whose content starts at from, or -1. In JSON-encoded
// text the shell's quote is the escape \" and the returned offset is that of
// its backslash.
func (l *pwLexer) scanDouble(from int) int {
	text := l.text
	for j := from; j < l.limit; j += l.unitLen(j) {
		if l.stops(j) {
			return -1
		}
		if l.json && text[j] == '\\' && j+1 < len(text) && text[j+1] == '"' {
			return j
		}
		if !l.json && text[j] == '"' {
			return j
		}
	}
	return -1
}

// scanUntil returns the offset of the first unit close after from, or -1
// when a unit open or a quote comes first: substitutions and expansions are
// not nested and hold no quotes.
func (l *pwLexer) scanUntil(from int, close, open byte) int {
	for j := from; j < l.limit; j += l.unitLen(j) {
		switch c := l.text[j]; {
		case l.stops(j) || c == open || c == '\'' || c == '"' || (c == '\\' && j+1 < len(l.text) && l.text[j+1] == '"'):
			return -1
		case c == close:
			return j
		}
	}
	return -1
}

// segments returns the run's segments without the empty quoted strings.
func (w pwWord) segments(run []pwSeg) []pwSeg {
	var out []pwSeg
	for _, g := range run {
		switch g.kind {
		case segSingle, segDouble:
			if g.ce == g.cs {
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

// shellRefRe matches a value that is wholly one shell reference: $NAME in the
// shell-variable shape, ${NAME}, ${NAME-} or ${NAME:-} (an empty default), or
// a command substitution $(...).
var shellRefRe = regexp.MustCompile(`^\$(?:[A-Z_][A-Z0-9_]*|\{[A-Za-z_][A-Za-z0-9_]*(?::?-)?\}|\([^()]*\))$`)

// pwPlaceholder reports whether v is a documentation placeholder, not a
// password: a template reference ({name}, {{ name }}, <name>, %(name)s,
// %NAME%), an upper-case placeholder shape (YOUR_PASSWORD), a placeholder word
// or the redaction marker itself.
func pwPlaceholder(v string) bool {
	if v == "[REDACTED]" || dbURLReferenceRe.MatchString(v) || placeholderShape(v) {
		return true
	}
	switch strings.ToLower(v) {
	case "password", "passwd", "secret", "changeme", "yourpassword", "pa55word":
		return true
	}
	return false
}

// pwPortRe is a container port mapping as docker and podman take it with -p:
// digits, dots and colons with at least one colon, and an optional protocol.
var pwPortRe = regexp.MustCompile(`^[0-9.:]*:[0-9.:]*(?:/(?:tcp|udp))?$`)

// holdsNoSecret reports whether the word is not a password: every run is
// empty, or exactly one run is not and it is wholly one reference, command
// substitution or placeholder (a single-quoted one only a placeholder, since
// the shell does not expand it), or, when port is set, a port mapping.
func (w pwWord) holdsNoSecret(port bool) bool {
	var only []pwSeg
	for _, r := range w.runs {
		segs := w.segments(r)
		if len(segs) == 0 {
			continue
		}
		if only != nil {
			return false
		}
		only = segs
	}
	if only == nil {
		return true
	}
	if port && w.portMapping(only) {
		return true
	}
	if len(only) != 1 {
		return false
	}
	g := only[0]
	switch g.kind {
	case segText, segSubst, segBrace:
		v := w.text[g.s:g.e]
		return shellRefRe.MatchString(v) || pwPlaceholder(v)
	case segDouble:
		v := w.text[g.cs:g.ce]
		return shellRefRe.MatchString(v) || pwPlaceholder(v)
	case segSingle:
		return pwPlaceholder(w.text[g.cs:g.ce])
	}
	return false
}

// portMapping reports whether the segments spell a port mapping in plain
// text and quotes.
func (w pwWord) portMapping(segs []pwSeg) bool {
	var b strings.Builder
	for _, g := range segs {
		switch g.kind {
		case segText:
			b.WriteString(w.text[g.s:g.e])
		case segSingle, segDouble:
			b.WriteString(w.text[g.cs:g.ce])
		default:
			return false
		}
	}
	return pwPortRe.MatchString(b.String())
}

// spans returns the raw spans that hide the word when it is a password:
// every non-empty stretch of each run between the quotes of its single- and
// double-quoted strings, so those quotes and the separators between runs
// stay. ANSI-C and locale quoting ($'...', $"...") is replaced whole.
func (w pwWord) spans(port bool) [][2]int {
	if w.holdsNoSecret(port) {
		return nil
	}
	var out [][2]int
	open := -1 // start of the stretch in progress
	flush := func(e int) {
		if open >= 0 && e > open {
			out = append(out, [2]int{open, e})
		}
		open = -1
	}
	for _, r := range w.runs {
		for _, g := range r {
			switch g.kind {
			case segSingle, segDouble:
				flush(g.s)
				if g.ce > g.cs {
					out = append(out, [2]int{g.cs, g.ce})
				}
			default:
				if open < 0 {
					open = g.s
				}
			}
		}
		if len(r) > 0 {
			flush(r[len(r)-1].e)
		}
	}
	return out
}

// shellPasswordMatches returns the spans of the MySQL/MariaDB client, sshpass,
// MYSQL_PWD and SSHPASS passwords.
func shellPasswordMatches(s *textScan) [][2]int {
	out := dbClientPasswordMatches(s)
	out = append(out, sshpassMatches(s)...)
	return append(out, envPasswordMatches(s)...)
}

// lineBreakAt reports whether text[i] is a line break that ends a line: a
// newline or carriage return that no backslash continues.
func lineBreakAt(text string, i int) bool {
	c := text[i]
	if c != '\n' && c != '\r' {
		return false
	}
	p := i
	if c == '\n' && p > 0 && text[p-1] == '\r' {
		p--
	}
	return !oddBackslashesBefore(text, 0, p)
}

// dbClientNames are the starts of the MySQL/MariaDB client names; a line
// that holds neither holds no client word.
var dbClientNames = []string{"mysql", "mariadb"}

// Client kinds: each has its own table of argument-less short options.
const (
	dbKindMysql = 1 << iota // mysql, mariadb
	dbKindDump              // mysqldump, mariadb-dump, mysqlpump
	dbKindAdmin             // mysqladmin, mariadb-admin
	dbKindOther             // any other client of the family
)

// The short options that take no argument, so they can stand before p in one
// option cluster (-sNpVALUE). The options that take a value (-u, -h, -P, -D,
// -e, -S, ...; for mysqldump also -T, -r and -w, for mysqladmin -c, -i and
// -w) are not among them: what follows one is its value. A client without a
// table of its own is read with the widest one.
const (
	dbMysqlFlags = "?ABCEGHIJKLNTUVXbcfgijklnoqrstvwx"
	dbDumpFlags  = dbMysqlFlags + "RFQYadey"
	dbAdminFlags = "?CEIVbfrsv"
)

// dbKindFlags pairs each client kind with its table.
var dbKindFlags = []struct {
	kind  uint8
	flags string
}{{dbKindMysql, dbMysqlFlags}, {dbKindDump, dbDumpFlags}, {dbKindAdmin, dbAdminFlags}, {dbKindOther, dbDumpFlags}}

// dbHyphenClients are the MariaDB client names spelled with a hyphen after
// "mariadb-".
var dbHyphenClients = map[string]bool{
	"access": true, "backup": true, "binlog": true, "check": true, "client-test": true, "conv": true,
	"convert-table-format": true, "dumpslow": true, "find-rows": true, "fix-extensions": true, "hotcopy": true,
	"import": true, "install-db": true, "plugin": true, "secure-installation": true, "setpermission": true,
	"show": true, "slap": true, "test": true, "tzinfo-to-sql": true, "upgrade": true, "waitpid": true,
}

// dbClientNameMax bounds the length of a client name.
const dbClientNameMax = 40

// dbClientKind returns the kind of the MySQL/MariaDB client that name is, in
// any letter case and with an optional .exe suffix, or 0: mysql or mariadb,
// alone or followed by letters, digits and underscores (mysqldump,
// mysqladmin, mysqlsh, mysql_upgrade, ...), or a hyphenated MariaDB client
// (mariadb-dump, mariadb-admin, mariadb-check, ...). A name that goes on with
// anything else (mysql.service, mysql-server, mysql:8) is not a client.
func dbClientKind(name string) uint8 {
	if len(name) < len("mysql") || len(name) > dbClientNameMax {
		return 0
	}
	l := strings.TrimSuffix(lowerASCII(name), ".exe")
	switch l {
	case "mysql", "mariadb":
		return dbKindMysql
	case "mysqldump", "mariadb-dump", "mysqlpump":
		return dbKindDump
	case "mysqladmin", "mariadb-admin":
		return dbKindAdmin
	}
	if rest, ok := strings.CutPrefix(l, "mariadb-"); ok && dbHyphenClients[rest] {
		return dbKindOther
	}
	for _, p := range dbClientNames {
		if rest, ok := strings.CutPrefix(l, p); ok && identWord(rest) {
			return dbKindOther
		}
	}
	return 0
}

// dbWrappers are the commands that run the command given after their own
// options, and the builtins that take NAME=value words: the word after them
// still stands in command position.
var dbWrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "exec": true, "time": true, "nice": true, "nohup": true,
	"command": true, "timeout": true,
	"export": true, "declare": true, "typeset": true, "local": true, "readonly": true, "alias": true,
}

// optionStartAt reports whether an option word starts at text[i]: text[i] is
// '-' and does not continue a word (the start of the text, after a byte that
// is not a word character, or after a JSON \n, \r or \t escape).
func optionStartAt(text string, i int) bool {
	return text[i] == '-' && (i == 0 || !isCommandWordByte(text[i-1]) || jsonBlankEscapeBefore(text, i))
}

// dbClientFlagMax bounds the argument-less flags read before p in one option
// cluster.
const dbClientFlagMax = 16

// dbClientFlagRun returns how many argument-less flags of any client t starts
// with, or 0 when more than dbClientFlagMax do.
func dbClientFlagRun(t string) int {
	n := 0
	for n < len(t) && strings.IndexByte(dbDumpFlags, t[n]) >= 0 {
		if n++; n > dbClientFlagMax {
			return 0
		}
	}
	return n
}

// dbClusterOf reports whether every flag of cluster comes from the table of
// one client kind among kinds.
func dbClusterOf(cluster string, kinds uint8) bool {
	for _, k := range dbKindFlags {
		if kinds&k.kind != 0 && strings.Trim(cluster, k.flags) == "" {
			return true
		}
	}
	return false
}

// dbQuoteDepth is the deepest the quotes of one line nest, the unquoted text
// included: ', " and the escaped \" of each JSON encoding depth read (an odd
// run of at most sshpassEscapeMax backslashes). A quote of a kind that is
// open closes it, so no kind is open twice.
const dbQuoteDepth = 3 + (sshpassEscapeMax+1)/2

// dbFrame is one open quote of a line (the first frame is the unquoted text)
// and the kinds of the client words that govern what follows inside it.
type dbFrame struct {
	quote int
	kinds uint8
}

// dbOption is one password option word of a line: its lexed value, the flags
// clustered before p, whether the value may be a port mapping, and the client
// kinds that govern it — by the separators read so far, and when none counts.
type dbOption struct {
	word       pwWord
	cluster    string
	port       bool
	kinds, any uint8
}

// dbLine reads one line for dbClientPasswordMatches.
type dbLine struct {
	text   string
	frames [dbQuoteDepth]dbFrame
	depth  int
	any    uint8 // every client word so far, whatever separates it
	// Command position: the next word is the command of a simple command,
	// also behind assignments and wrappers with their options.
	cmdPos, wrapper, afterOpt, named bool
	// pending is the kind of a NAME=client word in command position: it is a
	// client word when an option follows it directly.
	pending uint8
	tok     int // start of the word in progress, or -1
	opts    []dbOption
}

// quote reads a quote character of the given kind. One that matches an open
// quote closes it together with the quotes nested in it, and the client words
// read inside govern the rest of the enclosing text; any other opens a nested
// quote, whose first word stands in command position.
func (l *dbLine) quote(kind int) {
	for d := l.depth - 1; d >= 1; d-- {
		if l.frames[d].quote != kind {
			continue
		}
		for k := d; k < l.depth; k++ {
			l.frames[d-1].kinds |= l.frames[k].kinds
		}
		l.depth = d
		l.cmdPos, l.wrapper, l.afterOpt, l.named = false, false, false, false
		return
	}
	l.frames[l.depth] = dbFrame{quote: kind}
	l.depth++
	l.cmdPos, l.wrapper, l.afterOpt, l.named = true, false, false, false
}

// kinds returns the client kinds that govern the current position: the
// client words of the text around it at every quoting depth that no separator
// of their own depth has ended.
func (l *dbLine) kinds() uint8 {
	var k uint8
	for d := 0; d < l.depth; d++ {
		k |= l.frames[d].kinds
	}
	return k
}

// separator reads an unquoted command separator, or one inside a quote: it
// ends the simple command of the text at its own quoting depth, not that of
// the text around the quote.
func (l *dbLine) separator() {
	l.frames[l.depth-1].kinds = 0
	l.cmdPos, l.wrapper, l.afterOpt, l.named = true, false, false, false
}

// endWord judges the word text[l.tok:e], if one is in progress.
func (l *dbLine) endWord(e int) {
	if l.tok < 0 || l.tok >= e {
		l.tok = -1
		return
	}
	w := l.text[l.tok:e]
	l.tok = -1
	base := w[strings.LastIndexAny(w, `/\`)+1:]
	named := l.named
	l.named = false
	var kind, pending uint8
	switch {
	case w[0] == '-':
		// An option is no client word, except the value of --entrypoint,
		// which names the program a container runs.
		if v, ok := strings.CutPrefix(w, "--entrypoint="); ok {
			kind = dbClientKind(v[strings.LastIndexAny(v, `/\`)+1:])
		}
		l.named = w == "--entrypoint"
		if w == "--" {
			l.cmdPos, l.wrapper = true, false
		}
		l.afterOpt = l.wrapper
	case strings.IndexByte(w, '=') >= 0:
		// A word with '=' is an assignment: the command position stays as it
		// is. NAME=client in command position may be a command written
		// without its quotes (see pending).
		if eq := strings.IndexByte(w, '='); l.cmdPos && eq > 0 && identWord(w[:eq]) {
			pending = dbClientKind(w[eq+1:])
		}
	default:
		path := len(base) != len(w)
		if k := dbClientKind(base); k != 0 && (!path || l.cmdPos || named || strings.HasSuffix(lowerASCII(base), ".exe")) {
			kind = k
		}
		switch {
		case dbWrappers[base]:
			l.cmdPos, l.wrapper, l.afterOpt = true, true, false
		case l.wrapper && (l.afterOpt || (w[0] >= '0' && w[0] <= '9')):
			l.afterOpt = false // an option's value, or a duration or priority
		default:
			l.cmdPos, l.wrapper, l.afterOpt = false, false, false
		}
	}
	l.pending = pending
	l.client(kind)
}

// client records a client word of the given kind at the current position.
func (l *dbLine) client(kind uint8) {
	l.frames[l.depth-1].kinds |= kind
	l.any |= kind
}

// option reads the option word that starts at text[i] and returns where its
// value starts when it is a password option: -pVALUE, also after
// argument-less flags in one cluster, or --password=VALUE (or a prefix of it
// from --pas).
func (l *dbLine) option(i int) (vs int, cluster string, port bool) {
	text := l.text
	flags := i + 1 + dbClientFlagRun(text[i+1:])
	switch {
	case flags < len(text) && text[flags] == 'p':
		return flags + 1, text[i+1 : flags], true
	case text[i+1] == '-':
		k := i + 2
		for k < len(text) && k-i < 12 && (text[k] >= 'a' && text[k] <= 'z' || text[k] >= 'A' && text[k] <= 'Z') {
			k++
		}
		if name := strings.ToLower(text[i+2 : k]); k < len(text) && text[k] == '=' && len(name) >= 3 && strings.HasPrefix("password", name) {
			return k + 1, "", false
		}
	}
	return -1, "", false
}

// read reads the line text[ls:le] and returns the spans of its client
// passwords.
func (l *dbLine) read(ls, le int, quotes *jsonStringTracker, out [][2]int) [][2]int {
	text := l.text
	*l = dbLine{text: text, depth: 1, cmdPos: true, tok: -1, opts: l.opts[:0]}
	floor := ls // no option word starts before the end of the last password word
	for i := ls; i < le; {
		c := text[i]
		switch c {
		case '\\':
			k := i + 1
			for k < le && text[k] == '\\' {
				k++
			}
			n := k - i
			switch {
			case k >= le || n%2 == 0 || n > sshpassEscapeMax:
				// Backslashes of a path, or escaped ones.
				if l.tok < 0 {
					l.tok = i
				}
				i = k
				continue
			case text[k] == '"':
				l.endWord(i)
				l.quote(256 + n)
			case text[k] == 'n' || text[k] == 'r' || text[k] == 't' || text[k] == '\n' || text[k] == '\r':
				// A JSON blank escape or a line continuation separates words;
				// after an encoded line break a command starts.
				l.endWord(i)
				if text[k] == 'n' || text[k] == 'r' {
					l.cmdPos, l.wrapper, l.afterOpt = true, false, false
				}
			default:
				if l.tok < 0 {
					l.tok = i // the escaped byte belongs to the word
				}
			}
			i = k + 1
			continue
		case '\'', '"':
			l.endWord(i)
			l.quote(int(c))
		case ';', '|', '&':
			l.endWord(i)
			l.separator()
		case '(', '`':
			l.endWord(i)
			l.cmdPos, l.wrapper, l.afterOpt = true, false, false
		case ' ', '\t', '\n', '\r', ')', '<', '>', ',', '[', ']', '{', '}', '@', '$':
			l.endWord(i)
		default:
			if c == '-' && l.tok < 0 {
				l.client(l.pending)
			}
			if c == '-' && i >= floor && i+2 < len(text) && optionStartAt(text, i) {
				if vs, cluster, port := l.option(i); vs >= 0 {
					w := lexPasswordWord(text, vs, trackQuotes(quotes, text, i))
					l.opts = append(l.opts, dbOption{word: w, cluster: cluster, port: port, kinds: l.kinds(), any: l.any})
					floor = w.e
				}
			}
			if l.tok < 0 {
				l.tok = i
			}
		}
		i++
	}
	l.endWord(le)
	// A quote left open leaves the separators unreadable: none counts.
	unreadable := l.depth > 1
	for _, o := range l.opts {
		kinds := o.kinds
		if unreadable {
			kinds = o.any
		}
		if kinds == 0 || (o.cluster != "" && !dbClusterOf(o.cluster, kinds)) {
			continue
		}
		out = append(out, o.word.spans(o.port)...)
	}
	return out
}

// nextDBClientName returns the offset of the first MySQL/MariaDB client name
// at or after pos, in any letter case, or -1. next keeps, per name, the next
// occurrence found so far, so a sequence of calls reads the text once.
func nextDBClientName(s *textScan, pos int, next []int) int {
	best := -1
	for k, name := range dbClientNames {
		if next[k] != -1 && next[k] < pos {
			next[k] = s.indexFoldFrom(name, pos)
		}
		if next[k] >= 0 && (best < 0 || next[k] < best) {
			best = next[k]
		}
	}
	return best
}

// dbClientPasswordMatches returns the spans of MySQL/MariaDB client passwords:
// the value of an option word -pVALUE, also after argument-less flags in one
// cluster (-sNpVALUE), or --password=VALUE (or a prefix of it from --pas), at
// any quoting depth, when a client word governs it.
//
// A client word is a word that is exactly a client name (see dbClientKind),
// bare, in any letter case — also as the value of --entrypoint, and of
// NAME=client in command position when an option follows it directly — or a
// path whose last component is one when the path stands
// in command position (the first word of a simple command, of a quoted
// string or after "--", behind NAME=value words and the wrappers of
// dbWrappers with their options) or ends in .exe. Words end at blanks,
// quotes, separators and the characters ( ) < > , [ ] { } @ $, so a name
// inside a longer word (mysql.service, mysql-backup.tar, db-mysql-01,
// --db=mysql) and a path that is an argument (/var/lib/mysql) are not client
// words.
//
// A client word governs the option words after it on its line up to the next
// command separator (| ; & and their doubles) of its own quoting depth: a
// separator inside a quoted argument does not end the command the quote
// belongs to, and a separator after a quoted script does not reach into it.
// A line whose quotes do not pair is read as if it held no separator. A
// cluster is the client's only when each of its flags is an argument-less
// option of one governing client kind. An attached -p value that is a port
// mapping is not a password; a bare -p prompts and -P is a port.
func dbClientPasswordMatches(s *textScan) [][2]int {
	text := s.text
	var out [][2]int
	var quotes jsonStringTracker
	var line dbLine
	next := []int{-2, -2}
	for pos := 0; pos < len(text); {
		j := nextDBClientName(s, pos, next)
		if j < 0 {
			break
		}
		ls, le := j, j
		for ls > pos && !lineBreakAt(text, ls-1) {
			ls--
		}
		for le < len(text) && !lineBreakAt(text, le) {
			le++
		}
		line.text = text
		out = line.read(ls, le, &quotes, out)
		pos = le + 1
	}
	return out
}

// sshpassMaxWords bounds how many words after one sshpass word are read.
const sshpassMaxWords = 32

// sshpassEscapeMax bounds the backslashes read as one escape, which covers a
// quote or a line continuation encoded in JSON twice.
const sshpassEscapeMax = 8

// sshpassMatches returns the spans of sshpass passwords. Every sshpass word
// (in any letter case, behind a path, with an .exe suffix, quoted or not) is
// read on its own, also inside a quoted argument of another command (a jump
// host's ProxyCommand, ssh's remote command), by one rule that does not
// depend on the quoting around it. The words after it are whatever stands
// between separators (sshpassSep): blanks, quotes, commas, square brackets,
// line continuations and JSON blank escapes, so a command line, a JSON-encoded
// command and an argv list read alike. The first -p takes the password,
// attached (-pVALUE) or as the next word, and the reading ends there, so the
// wrapped command's own -p (an ssh port) is not sshpass's. It ends without a
// password at -f, -d or -e (the password comes from elsewhere), at "--" and
// at the first word that names a program (letters, digits and _ . / + ~ -).
// -P takes a value; any other option, and a word that is neither an option
// nor a program name, is passed over.
func sshpassMatches(s *textScan) [][2]int {
	text := s.text
	var out [][2]int
	var quotes jsonStringTracker
	for pos := 0; pos < len(text); {
		j := s.indexFoldFrom("sshpass", pos)
		if j < 0 {
			break
		}
		pos = j + len("sshpass")
		if j > 0 && isCommandWordByte(text[j-1]) && !jsonBlankEscapeBefore(text, j) {
			continue
		}
		after := pos
		if len(text)-after >= 4 && strings.EqualFold(text[after:after+4], ".exe") {
			after += 4
		}
		if after < len(text) && isCommandWordByte(text[after]) {
			continue
		}
		quotes.at(text, j)
		out = append(out, sshpassPasswordSpans(text, after, quotes)...)
	}
	return out
}

// sshpassSep returns the length of the word separator at text[i], or 0, and
// whether it is a quote. A separator is a blank, a comma, a square bracket, a
// quote (' or ", also escaped as \"), a line continuation (a backslash and a
// line break) or a JSON \n, \r or \t escape; the escaped forms may carry more
// backslashes, as JSON encoding adds them. The bracket that opens the
// redaction marker belongs to it, so a replaced password reads as one word.
func sshpassSep(text string, i int) (n int, quote bool) {
	switch text[i] {
	case '[':
		if strings.HasPrefix(text[i:], "[REDACTED]") {
			return 0, false
		}
		return 1, false
	case ' ', '\t', ',', ']':
		return 1, false
	case '\'', '"':
		return 1, true
	case '\\':
		k := i + 1
		for k < len(text) && k-i < sshpassEscapeMax && text[k] == '\\' {
			k++
		}
		if k == len(text) {
			return 0, false
		}
		switch text[k] {
		case '"':
			return k + 1 - i, true
		case 'n', 'r', 't', '\n':
			return k + 1 - i, false
		case '\r':
			if k+1 < len(text) && text[k+1] == '\n' {
				k++
			}
			return k + 1 - i, false
		}
	}
	return 0, false
}

// sshpassSkip returns the offset after the separators at text[i:]; quotes
// tells whether quotes are skipped too.
func sshpassSkip(text string, i int, quotes bool) int {
	for i < len(text) {
		n, quote := sshpassSep(text, i)
		if n == 0 || (quote && !quotes) {
			break
		}
		i += n
	}
	return i
}

// sshpassWordEnd returns the end of the word at text[i:]: the next separator
// or line break.
func sshpassWordEnd(text string, i int) int {
	for i < len(text) && text[i] != '\n' && text[i] != '\r' {
		if n, _ := sshpassSep(text, i); n > 0 {
			break
		}
		i++
	}
	return i
}

// programName reports whether a word that is not an option names a program:
// letters, digits and _ . / + ~ -.
func programName(w string) bool {
	for i := 0; i < len(w); i++ {
		if c := w[i]; !isIdentByte(c) && strings.IndexByte("./+~-", c) < 0 {
			return false
		}
	}
	return true
}

// sshpassPasswordSpans returns the password spans of the sshpass word that
// ends at text[from]. quotes is the quote tracker at that word; a copy
// follows the text up to the password.
func sshpassPasswordSpans(text string, from int, quotes jsonStringTracker) [][2]int {
	i := from
	for n := 0; n < sshpassMaxWords; n++ {
		s := sshpassSkip(text, i, true)
		e := sshpassWordEnd(text, s)
		if e == s {
			return nil // the line ended
		}
		w := text[s:e]
		i = e
		if w[0] != '-' {
			if programName(w) && !sshpassKeyEnd(text, e) {
				return nil
			}
			continue
		}
		if w == "--" {
			return nil
		}
	cluster:
		for k := 1; k < len(w); k++ {
			switch c := w[k]; {
			case c == 'p':
				return sshpassValue(text, s, s+k+1, e, quotes)
			case c == 'f' || c == 'd' || c == 'e':
				return nil
			case c == 'P':
				if k+1 == len(w) { // the prompt is the next word
					i = sshpassWordEnd(text, sshpassSkip(text, e, true))
				}
				break cluster
			case c < 'A' || c > 'z' || (c > 'Z' && c < 'a'):
				break cluster // not an option letter
			}
		}
	}
	return nil
}

// sshpassKeyEnd reports whether the word that ends at text[e] is a quoted
// mapping key (a quote and a ':' follow it), as in
// {"command":"sshpass","args":["-p","VALUE"]}: the argument list comes after.
func sshpassKeyEnd(text string, e int) bool {
	if e >= len(text) {
		return false
	}
	n, quote := sshpassSep(text, e)
	return quote && e+n < len(text) && text[e+n] == ':'
}

// sshpassValue returns the spans of the password of a -p that stands in the
// word text[s:e] with text[v:e] attached to it. With nothing attached the
// password is the next word, unless a quote opens right after an unquoted
// -p, which attaches a quoted value (-p"VALUE"); the quote that closes a
// quoted "-p" is passed, and what follows is an item of an argument list.
// The password is read as one shell word, whatever its shape.
func sshpassValue(text string, s, v, e int, quotes jsonStringTracker) [][2]int {
	list := false
	if v == e {
		opened := s > 0 && (text[s-1] == '"' || text[s-1] == '\'')
		n, quote := 0, false
		if e < len(text) {
			n, quote = sshpassSep(text, e)
		}
		if quote && opened {
			v, list = e+n, true
		}
		if !quote || opened {
			v = sshpassSkip(text, v, false)
		}
	}
	return lexPasswordItem(text, v, trackQuotes(&quotes, text, v), list).spans(false)
}

// envPasswordNames are the environment variables that carry a password for a
// client: MYSQL_PWD (the MySQL/MariaDB clients), in any letter case, and
// SSHPASS (sshpass -e), in upper case only, since the lower-case word is the
// command.
var envPasswordNames = []struct {
	name string
	fold bool
}{{"mysql_pwd", true}, {"SSHPASS", false}}

// envPasswordMatches returns the spans of MYSQL_PWD and SSHPASS values, at
// any value length. The value follows the name
//
//   - as a shell assignment (NAME=VALUE): one shell word, whatever its shape;
//   - as a mapping entry or an assignment with blanks, whether the name is
//     quoted or subscripted (NAME: VALUE, "NAME": "VALUE",
//     environ["NAME"] = "VALUE", "NAME" => "VALUE"): a quoted value, or an
//     unquoted one that is not code (see envCodeValue);
//   - as the quoted argument after the quoted name in a call
//     (setenv("NAME", "VALUE")): a callee and its opening parenthesis stand
//     before the name on the line;
//   - under the "value" key of an environment entry, after the name in JSON
//     ({"name":"NAME","value":"VALUE"}) or on the next line of a YAML list
//     item (- name: NAME / value: VALUE); a "valueFrom" key names a source,
//     not a password.
func envPasswordMatches(s *textScan) [][2]int {
	text := s.text
	var out [][2]int
	for _, name := range envPasswordNames {
		var quotes jsonStringTracker
		for pos := 0; pos < len(text); {
			j := -1
			if name.fold {
				j = s.indexFoldFrom(name.name, pos)
			} else if k := strings.Index(text[pos:], name.name); k >= 0 {
				j = pos + k
			}
			if j < 0 {
				break
			}
			pos = j + len(name.name)
			if j > 0 && isIdentByte(text[j-1]) && !jsonBlankEscapeBefore(text, j) {
				continue
			}
			first, second, list := envPasswordValue(text, j, pos)
			for _, vs := range []int{first, second} {
				if vs < 0 {
					continue
				}
				w := lexPasswordItem(text, vs, trackQuotes(&quotes, text, vs), list)
				out = append(out, w.spans(false)...)
				if w.e > pos {
					pos = w.e
				}
			}
		}
	}
	return out
}

// envPasswordValue returns where the value of the name text[j:i] starts, or
// -1, and whether it is the value of a mapping entry or a call argument
// rather than of a shell assignment. A second start is the value of a typed
// declaration (NAME: type = VALUE), whose type is replaced with it.
func envPasswordValue(text string, j, i int) (first, second int, list bool) {
	if i < len(text) && text[i] == '=' {
		return i + 1, -1, false
	}
	quoted := false
	if i < len(text) {
		if n, quote := sshpassSep(text, i); quote {
			i, quoted = i+n, true
		}
	}
	if i < len(text) && text[i] == ']' {
		i++
	}
	i = skipBlanks(text, i)
	if i >= len(text) {
		return -1, -1, false
	}
	switch c := text[i]; c {
	case '=', ':':
		// Also ==, :=, => and the like.
		for i++; i < len(text) && (text[i] == '=' || text[i] == '>'); i++ {
		}
		i = skipBlanks(text, i)
		if templateExpression(text, i) {
			return -1, -1, false
		}
		first, second = envMappingValue(text, i, c == ':')
		return first, second, true
	case ',':
		i = skipBlanks(text, i+1)
		if !quoted || i >= len(text) {
			return -1, -1, false
		}
		n, quote := sshpassSep(text, i)
		if !quote {
			return -1, -1, false
		}
		if strings.HasPrefix(text[i+n:], "value") {
			// The next member of an environment entry: its key, not a value.
			if v := envEntryValue(text, i+n); v >= 0 {
				first, second = envMappingValue(text, v, true)
				return first, second, true
			}
			if k := i + n + len("value"); k < len(text) && isIdentByte(text[k]) {
				return -1, -1, false
			}
		}
		if !callArgument(text, j) {
			return -1, -1, false
		}
		return i, -1, true
	case '\n', '\r', '\\':
		// The next line of a YAML list item, also in JSON-encoded text.
		if !yamlNameKey(text, j) {
			return -1, -1, false
		}
		switch {
		case strings.HasPrefix(text[i:], "\r\n"), strings.HasPrefix(text[i:], `\n`):
			i += 2
		case strings.HasPrefix(text[i:], `\r\n`):
			i += 4
		case c == '\\':
			return -1, -1, false
		default:
			i++
		}
		if v := envEntryValue(text, skipBlanks(text, i)); v >= 0 {
			first, second = envMappingValue(text, v, true)
			return first, second, true
		}
	}
	return -1, -1, false
}

// envEntryValue returns where the value of the key "value" starts when
// text[i:] is that key, with or without its closing quote, a ':' and the
// value, or -1.
func envEntryValue(text string, i int) int {
	if !strings.HasPrefix(text[i:], "value") {
		return -1
	}
	i += len("value")
	if i < len(text) {
		if n, quote := sshpassSep(text, i); quote {
			i += n
		}
	}
	i = skipBlanks(text, i)
	if i >= len(text) || text[i] != ':' {
		return -1
	}
	return skipBlanks(text, i+1)
}

// envLookBehind bounds how far back from a name its callee or its "name" key
// is searched.
const envLookBehind = 256

// callArgument reports whether the quoted name that starts at text[j] is an
// argument of a call: on its line an opening parenthesis that is still open
// stands before it, with the callee's name directly before the parenthesis.
func callArgument(text string, j int) bool {
	closed := 0
	for k := j - 1; k >= 0 && j-k <= envLookBehind && text[k] != '\n' && text[k] != '\r'; k-- {
		switch text[k] {
		case ')':
			closed++
		case '(':
			if closed > 0 {
				closed--
				continue
			}
			return k > 0 && isIdentByte(text[k-1])
		}
	}
	return false
}

// yamlNameKey reports whether the name that starts at text[j] is the value of
// a "name" key (name: NAME, name: "NAME").
func yamlNameKey(text string, j int) bool {
	k := j
	for k > 0 && j-k < envLookBehind && strings.IndexByte(" \t\"'\\", text[k-1]) >= 0 {
		k--
	}
	if k == 0 || text[k-1] != ':' {
		return false
	}
	k--
	for k > 0 && j-k < envLookBehind && strings.IndexByte(" \t\"'\\", text[k-1]) >= 0 {
		k--
	}
	return strings.HasSuffix(text[:k], "name") && (k == 4 || !isIdentByte(text[k-5]))
}

// envMappingValue judges the value of a mapping entry that starts at text[i]
// and returns where it starts, or -1 when it holds no password: a comment, a
// YAML tag (after a ':'), or code (see envCodeValue). In a typed declaration
// (NAME: type = VALUE) the value after the '=' is judged, and both the type
// and the value are returned when it is not code. A value that starts with a
// quote, a reference or a template is left to the word's own judgment.
func envMappingValue(text string, i int, colon bool) (first, second int) {
	if i >= len(text) || strings.IndexByte("\"'`\\$%{<[", text[i]) >= 0 {
		return i, -1
	}
	word := envValueWord(text, i)
	if text[i] == '#' || (colon && yamlTagRe.MatchString(word)) {
		return -1, -1
	}
	if k := skipBlanks(text, i+len(word)); colon && k > i+len(word) && k < len(text) && text[k] == '=' && (k+1 == len(text) || text[k+1] != '=') {
		v := skipBlanks(text, k+1)
		if v >= len(text) || text[v] == '\n' || text[v] == '\r' || envCodeValue(text, v) {
			return -1, -1
		}
		return i, v
	}
	if envCodeValue(text, i) {
		return -1, -1
	}
	return i, -1
}

// yamlTagRe matches a YAML tag (!vault, !secret, !!str).
var yamlTagRe = regexp.MustCompile(`^!{1,2}[A-Za-z][A-Za-z0-9_.:/-]*$`)

// envValueWord returns the unquoted word that starts at text[i]: up to the
// next blank or line break, and at most credentialJudgeWindow bytes.
func envValueWord(text string, i int) string {
	e := i
	for e < len(text) && e-i < credentialJudgeWindow && strings.IndexByte(" \t\n\r", text[e]) < 0 {
		e++
	}
	return text[i:e]
}

// envCodeValue reports whether the unquoted value that starts at text[i] is
// code rather than a literal, by the judgment of credential-named
// assignments (codeExpression): a keyword (null, None, nil, true, false), a
// member chain (cfg.Password), an environment lookup, a call or a subscript,
// also right before the bracket that closes the enclosing literal. A call or
// a subscript counts only when its closing bracket ends the word or the
// line, so a literal that merely holds a parenthesis is not one.
func envCodeValue(text string, i int) bool {
	word := envValueWord(text, i)
	if strings.IndexByte("\"'`\\$%{<[", text[i]) >= 0 {
		return false
	}
	t := strings.TrimRight(word, ";,")
	if u := strings.TrimRight(t, "}])"); !codeExpression(t) && (u == "" || u == t || !codeExpression(u)) {
		return false
	}
	if !callRe.MatchString(t) && !subscriptRe.MatchString(t) {
		return true
	}
	if strings.HasSuffix(t, ")") || strings.HasSuffix(t, "]") {
		return true
	}
	line := text[i:]
	if len(line) > credentialJudgeWindow {
		line = line[:credentialJudgeWindow]
	}
	e := strings.IndexAny(line, "\n\r")
	if e < 0 && len(line) == credentialJudgeWindow {
		return false
	}
	if e >= 0 {
		line = line[:e]
	}
	line = strings.TrimRight(line, " \t;,")
	return strings.HasSuffix(line, ")") || strings.HasSuffix(line, "]")
}

// templateMax bounds the length of one template expression.
const templateMax = 256

// templateExpression reports whether text[i:] is one unquoted template
// expression that ends its line ({{ .Values.db.password }}): it holds blanks,
// so it is not one shell word, and it names a value rather than carrying one.
func templateExpression(text string, i int) bool {
	if !strings.HasPrefix(text[i:], "{{") {
		return false
	}
	rest := text[i:]
	if len(rest) > templateMax {
		rest = rest[:templateMax]
	}
	k := strings.Index(rest, "}}")
	if k < 0 || strings.ContainsAny(rest[:k], "\n\r") {
		return false
	}
	return i+k+2 == len(text) || text[i+k+2] == '\n' || text[i+k+2] == '\r'
}

// skipBlanks returns the offset after the spaces and tabs at text[i:].
func skipBlanks(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}

// isIdentByte reports whether c is a letter, a digit or '_'.
func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// identWord reports whether s holds only letters, digits and '_'.
func identWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}
