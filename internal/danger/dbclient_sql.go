package danger

import (
	"regexp"
	"strings"
)

// dbClientPipePattern matches the psql meta-commands that pipe into a program
// (\g, \gx, \o, \w, \copy, \watch followed by a pipe) and \copy ... program
// 'cmd', which runs cmd locally without any pipe character. The backslash is
// optional for copy because normalization folds it away inside double quotes;
// a server-side COPY ... PROGRAM executes on the database host, which is no
// safer to wave through. It is matched on the raw text because the program is
// usually the quoted argument.
//
// Normalization also folds the backslash of \g, \o, \w and \watch away inside
// double quotes, so the bare word followed by a single pipe counts as well.
var dbClientPipePattern = regexp.MustCompile(`(?i)\\(?:g|gx|o|w|copy|watch)\b[^|]*\||(^|[\s;\\])copy\b[^\n]*\bprogram\b|(?:^|[\s;])(?:g|gx|o|w|watch)\s*\|(?:[^|]|$)`)

// dbClientCommandPattern matches the commands that run a local program from a
// command position, on text with SQL literals and comments removed: the
// clients' \!, mysql's \P, and mysql's system and pager commands, which count
// only at the start of the text, after a semicolon or newline, or after -e /
// --execute= (so `select * from system` is a query).
var dbClientCommandPattern = regexp.MustCompile(`(?i)\\!|(?:^|[;\n=]|-e)\s*(?:system|pager)\s|(?-i:\\P)\s`)

// dbClientShellText reports whether text carries a local-program escape at a
// command position (not inside an SQL literal or comment).
func dbClientShellText(text string) bool {
	if dbClientPipePattern.MatchString(text) {
		return true
	}
	// Comment syntax differs between psql (-- and /* */) and mysql (also #),
	// and an apostrophe inside a comment would otherwise pair with a later
	// quote and hide a real escape, so every reading is tried.
	for _, mode := range [][2]bool{{false, false}, {true, false}, {true, true}} {
		if dbClientCommandPattern.MatchString(sqlLiteralsStripped(text, mode[0], mode[1])) {
			return true
		}
	}
	return false
}

// sqlLiteralsStripped returns text with string literals, quoted identifiers,
// dollar-quoted bodies and (as selected) comments removed. A literal that is
// never closed is kept whole, so an unbalanced quote cannot hide what follows.
func sqlLiteralsStripped(text string, sqlComments, hashComments bool) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\'' || c == '"':
			end := closingQuote(text, i)
			if end < 0 {
				out.WriteString(text[i:])
				return out.String()
			}
			out.WriteByte(' ')
			i = end + 1
		case c == '$':
			tag := dollarTag(text[i:])
			if tag == "" {
				out.WriteByte(c)
				i++
				continue
			}
			end := strings.Index(text[i+len(tag):], tag)
			if end < 0 {
				out.WriteString(text[i:])
				return out.String()
			}
			out.WriteByte(' ')
			i += len(tag) + end + len(tag)
		case sqlComments && c == '-' && strings.HasPrefix(text[i:], "--"):
			i = skipToLineEnd(text, i)
		case hashComments && c == '#':
			i = skipToLineEnd(text, i)
		case sqlComments && c == '/' && strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				out.WriteString(text[i:])
				return out.String()
			}
			out.WriteByte(' ')
			i += 2 + end + 2
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// closingQuote returns the index of the quote closing the literal opened at
// text[open], treating a doubled quote as part of the literal, or -1.
func closingQuote(text string, open int) int {
	q := text[open]
	for i := open + 1; i < len(text); i++ {
		if text[i] != q {
			continue
		}
		if i+1 < len(text) && text[i+1] == q {
			i++
			continue
		}
		return i
	}
	return -1
}

// dollarTag returns the leading $tag$ delimiter of text, or "".
func dollarTag(text string) string {
	for i := 1; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '$':
			return text[:i+1]
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || i > 1 && c >= '0' && c <= '9':
		default:
			return ""
		}
	}
	return ""
}

// skipToLineEnd returns the index of the newline ending the line holding
// text[from], or len(text); the newline itself is kept.
func skipToLineEnd(text string, from int) int {
	if n := strings.IndexByte(text[from:], '\n'); n >= 0 {
		return from + n
	}
	return len(text)
}
