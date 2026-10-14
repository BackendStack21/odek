package redact

import (
	"regexp"
	"strings"
)

// passwordPunct is the punctuation generated passwords commonly carry. It
// is deliberately narrower than "anything": '.', ':', quotes, brackets and
// parentheses mark code, paths and URLs.
const passwordPunct = "!@#$%^&*"

// credentialNameRe finds a name containing a secret word followed by an
// assignment separator (=, :, =>), tolerating a closing quote after the
// name (JSON) and whitespace around the separator. Group 1 is the name.
var credentialNameRe = regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(?:token|secret|passw(?:or)?d|credential|api[_-]?key|access[_-]?key|private[_-]?key)[A-Za-z0-9_-]*)(?:\\?["'\x60])?\s*(?:=>|[:=])\s*`)

// keyedNameRe matches the names whose value is always treated as a
// credential, wherever the name begins: it ends with one of these words.
var keyedNameRe = regexp.MustCompile(`(?i)(?:api[_-]?key|api[_-]?secret|auth[_-]?token|access[_-]?token|refresh[_-]?token|bearer[_-]?token|client[_-]?secret|private[_-]?key|secret[_-]?key|secret[_-]?access[_-]?key|session[_-]?token|security[_-]?token|password|passwd)$`)

var camelSecretRe = regexp.MustCompile(`(?i)(?:token|secret|passw(?:or)?d|credentials?|apikey|accesskey|privatekey)$`)

var passwordNameRe = regexp.MustCompile(`(?i)(?:password|passwd)$`)

// Credential-name classes.
const (
	nameNone  = iota // not a credential name (or a bare word handled elsewhere)
	nameKeyed        // value is a credential whenever it is a literal
	nameLoose        // secret word is not the last segment: value must also have a generated-credential shape
)

// classifyCredentialName decides how a captured name is treated.
//
// A name that ends in a secret word (api_key, access_token, password, …)
// is keyed. A multi-segment all-upper (env style) or all-lower (snake or
// kebab style) name with a secret word in any other position
// (SECRET_KEY_BASE, TOKEN_GITHUB, api_key_prod) is loose. Mixed-case
// identifiers (camelCase, Max_Token_Count) and bare single words are code
// or are handled by the guarded bare-key rules. The secret words are the
// ones sensitiveName recognises, minus the over-broad bare KEY and PASS.
func classifyCredentialName(name string) int {
	name = strings.TrimLeft(name, "-")
	if keyedNameRe.MatchString(name) {
		return nameKeyed
	}
	if !strings.ContainsAny(name, "_-") {
		// camelCase / PascalCase ending in a secret word (GithubToken): a
		// secret only when the value also has a generated-credential shape.
		if i := camelSecretRe.FindStringIndex(name); i != nil && i[0] > 0 {
			return nameLoose
		}
		return nameNone
	}
	if name != strings.ToUpper(name) && name != strings.ToLower(name) {
		return nameNone
	}
	segs := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool { return r == '_' || r == '-' })
	for i, seg := range segs {
		word := false
		switch seg {
		case "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "CREDENTIALS", "APIKEY":
			word = true
		case "KEY":
			word = i > 0 && (segs[i-1] == "API" || segs[i-1] == "ACCESS" || segs[i-1] == "PRIVATE")
		}
		if word {
			if i == len(segs)-1 {
				return nameKeyed
			}
			return nameLoose
		}
	}
	return nameNone
}

var (
	envRefRe      = regexp.MustCompile(`^(?:\$(?:\{[^}\s]*\}?|\([^)]*\)?|[A-Za-z_][A-Za-z0-9_]*)|%[A-Za-z_][^%\s]*%|%\(\w+\)s|\{\{.*\}\}|<%.*%>)$`)
	callRe        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*\(`)
	subscriptRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*\[`)
	memberChainRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
)

// codeExpression reports whether an unquoted value is a code expression, an
// environment/variable reference or a keyword literal rather than a literal
// secret: ${VAR}, $VAR, %VAR%, os.environ[...], process.env.X, ENV[...],
// getenv(...), function calls, subscripts, member chains (a.b.c) and
// null/None/nil/true/false.
func codeExpression(v string) bool {
	t := strings.TrimRight(v, ";,")
	if t == "" {
		return true
	}
	lower := strings.ToLower(t)
	switch lower {
	case "null", "none", "nil", "true", "false", "undefined", "nan":
		return true
	}
	for _, p := range []string{"$env:", "os.environ", "process.env", "os.getenv", "getenv", "system.getenv", "env[", "env.", "environ["} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return envRefRe.MatchString(t) || callRe.MatchString(t) || subscriptRe.MatchString(t) || memberChainRe.MatchString(t)
}

// quotedReference reports whether a quoted value is only a variable or
// template reference ("${DB_PASSWORD}", "$DB_PASSWORD", "{{ secret }}").
func quotedReference(v string) bool {
	t := strings.TrimSpace(v)
	if strings.HasPrefix(t, "$(") && strings.HasSuffix(t, ")") ||
		strings.HasPrefix(t, "${") && strings.HasSuffix(t, "}") ||
		strings.HasPrefix(t, "{{") && strings.HasSuffix(t, "}}") {
		return true
	}
	return strings.HasPrefix(t, "$") && envRefRe.MatchString(t) && strings.ToUpper(t) == t
}

// maxCredentialValueCheck bounds how much of a value is examined and how far
// a closing quote is searched for. A longer value is still redacted whole
// (its end is found with a single linear scan) but is judged on its first
// maxCredentialValueCheck bytes.
const maxCredentialValueCheck = 4096

// credentialJudgeWindow is how much of an unquoted value is examined to
// decide whether it is a secret. A longer value is judged on its first
// credentialJudgeWindow bytes (its end is found by a separate linear scan
// when it is accepted), so a rejected value costs a bounded, small amount.
const credentialJudgeWindow = 256

// parseCredentialValue reads the value starting at pos: a quoted literal up
// to its closing quote on the same line (a backslash-escaped quote does not
// close it), or an unquoted run to the next whitespace, cut at `,"` (the
// start of the next JSON member; inside a JSON string also at `,\"`, the next
// member of JSON encoded in that string) and at a } or ] that closes a structure
// opened before the name (curly and square are the open counts at the name).
// Any other bracket belongs to the value, so a secret that contains one is
// kept whole. An opening quote without a closing one starts an unquoted run.
//
// It returns the value to judge (without quotes, at most
// maxCredentialValueCheck bytes), the end offset of the token, and whether it
// was a closed quoted literal. Unless full is set, an unquoted run is scanned
// only as far as judging needs; complete reports whether end is the real end
// of the token. A rejected value therefore never costs more than the bounded
// window, however long the rest of the line is.
//
// Inside a JSON string (json) the text is JSON-encoded: a value wrapped in
// escaped quotes (\"...\") is a quoted literal, and an unquoted run also ends
// at a \n, \r, \t, \f or \b escape and at the unescaped quote that closes the
// JSON string. Any other escape, \" included, is part of the value.
func parseCredentialValue(text string, pos int, curly, square int, full, json bool) (val string, end int, quoted, complete bool) {
	if pos >= len(text) {
		return "", pos, false, true
	}
	if json && strings.HasPrefix(text[pos:], `\"`) {
		if v, e, ok := jsonEscapedQuoted(text, pos); ok {
			return clipCredentialValue(v), e, true, true
		}
		pos += 2
	} else if c := text[pos]; c == '"' || c == '\'' || c == '`' {
		rest := text[pos+1:]
		limit := len(rest)
		if limit > maxCredentialValueCheck {
			limit = maxCredentialValueCheck
		}
		if nl := strings.IndexByte(rest[:limit], '\n'); nl >= 0 {
			limit = nl
		}
		for i := 0; i < limit; i++ {
			switch rest[i] {
			case '\\':
				i++
			case c:
				return rest[:i], pos + 1 + i + 1, true, true
			}
		}
		pos++
	}
	stop := len(text)
	if !full && stop-pos > credentialJudgeWindow {
		stop = pos + credentialJudgeWindow
	}
	lc, ls := 0, 0 // brackets opened inside the value
	for end = pos; end < stop; end++ {
		switch text[end] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			return clipCredentialValue(text[pos:end]), end, false, true
		case '{':
			lc++
		case '[':
			ls++
		case '}':
			if lc > 0 {
				lc--
			} else if curly > 0 {
				return closeBracketValue(text, pos, end)
			}
		case ']':
			if ls > 0 {
				ls--
			} else if square > 0 {
				return closeBracketValue(text, pos, end)
			}
		case ',':
			if end+1 < len(text) && text[end+1] == '"' || json && strings.HasPrefix(text[end+1:], `\"`) {
				return clipCredentialValue(text[pos:end]), end, false, true
			}
		case '\\':
			if json && end+1 < len(text) {
				if jsonBlankEscape(text[end+1]) {
					return clipCredentialValue(text[pos:end]), end, false, true
				}
				end++ // the escaped character belongs to the value
			}
		case '"':
			if json && jsonStringEnds(text, end+1) {
				return clipCredentialValue(text[pos:end]), end, false, true
			}
		}
	}
	return clipCredentialValue(text[pos:end]), end, false, end == len(text)
}

// jsonBlankEscape reports whether c, after a backslash in JSON-encoded text,
// encodes a line break, tab or other blank control character.
func jsonBlankEscape(c byte) bool {
	return c == 'n' || c == 'r' || c == 't' || c == 'f' || c == 'b'
}

// jsonStringEnds reports whether the unescaped double quote just before i
// closes a JSON string: it is followed, after optional blanks, by the end of
// the text or line or by a JSON structural character. A quote followed by
// anything else is an ordinary character of the surrounding text.
func jsonStringEnds(text string, i int) bool {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i == len(text) || strings.IndexByte(",:}]\r\n", text[i]) >= 0
}

// jsonEscapedQuoted reads a literal wrapped in escaped quotes (\"...\") that
// starts at text[pos] inside a JSON string (see escapedQuoteClose), at most
// maxCredentialValueCheck bytes long.
func jsonEscapedQuoted(text string, pos int) (string, int, bool) {
	start := pos + 2
	limit := len(text)
	if limit-start > maxCredentialValueCheck {
		limit = start + maxCredentialValueCheck
	}
	if i, ok := escapedQuoteClose(text, start, limit); ok {
		return text[start:i], i + 2, true
	}
	return "", 0, false
}

// escapedQuoteClose finds the escaped quote (\") that closes a literal opened
// by an escaped quote just before start, in JSON-encoded text. The text is
// read one encoded character at a time (a backslash pair is one character),
// and the literal's own escapes are tracked on the decoded characters: a
// decoded backslash escapes the next one, so \\\" (an escaped quote inside
// the literal) belongs to it while \\\\\" (an escaped backslash, then the
// closing quote) ends it. It fails at the raw quote that ends the enclosing
// JSON string, at a raw or encoded line break and at limit.
func escapedQuoteClose(text string, start, limit int) (int, bool) {
	inner := false // the previous decoded character was an unescaped backslash
	for i := start; i < limit; {
		switch c := text[i]; c {
		case '"', '\n', '\r':
			return 0, false
		case '\\':
			if i+1 >= len(text) {
				return 0, false
			}
			switch d := text[i+1]; {
			case d == 'n' || d == 'r':
				return 0, false
			case inner:
				inner = false
			case d == '\\':
				inner = true
			case d == '"':
				return i, true
			}
			i += 2
		default:
			inner = false
			i++
		}
	}
	return 0, false
}

// closeBracketValue ends a value at the closing bracket at end; a quote just
// before it closes an enclosing JSON string and is left out of the value.
func closeBracketValue(text string, pos, end int) (string, int, bool, bool) {
	if end > pos && text[end-1] == '"' {
		end--
	}
	return clipCredentialValue(text[pos:end]), end, false, true
}

func clipCredentialValue(v string) string {
	if len(v) > maxCredentialValueCheck {
		return v[:maxCredentialValueCheck]
	}
	return v
}

// credentialValueOK is the single value test for credential-named
// assignments: a literal of sufficient length that is not a code
// expression. Password-like names accept a shorter literal (8+) when it
// carries a digit or password punctuation, so `password=hunter2!@#Secret`
// is covered while prose such as `password: required` is not.
func credentialValueOK(v string, quoted, passwordName bool) bool {
	if v == "" || strings.HasPrefix(v, "[REDACTED]") {
		return false
	}
	minLen := 20
	if passwordName {
		minLen = 8
	}
	if len(v) < minLen {
		return false
	}
	if len(v) < 20 && !strings.ContainsAny(v, "0123456789"+passwordPunct) {
		return false
	}
	if strings.Count(v, v[:1]) == len(v) { // mask such as ********
		return false
	}
	if quoted {
		return !quotedReference(v) && !secretPathReference(v)
	}
	return !codeExpression(v) && !sourceValue(v) && !secretPathReference(v)
}

// referenceValue reports whether a rejected value is a variable, template or
// secret-store reference that is skipped whole.
func referenceValue(v string, quoted bool) bool {
	if v == "" {
		return false
	}
	if quoted {
		return quotedReference(v) || secretPathReference(v)
	}
	return envRefRe.MatchString(strings.TrimRight(v, ";,")) || secretPathReference(v)
}

// secretPathReference reports whether v points at a secret store: an
// @env:NAME reference or a home-relative path.
func secretPathReference(v string) bool {
	return strings.HasPrefix(v, "@env:") || strings.HasPrefix(v, "~/")
}

var (
	sourceIdentRe     = regexp.MustCompile(`^[A-Za-z_]+$`)
	camelTransitionRe = regexp.MustCompile(`[a-z][A-Z]`)
)

// sourceValue reports whether an unquoted value reads as source code rather
// than a literal secret: an address-of or dereference (&password,
// *body.Password), an object or composite literal opening, or an identifier
// or selector expression that ends a struct field or call (cfg.Password,
// passwordFromVault)).
func sourceValue(v string) bool {
	switch v[0] {
	case '&', '*', '{':
		return true
	}
	t := strings.TrimRight(v, ",);")
	if t == v || t == "" {
		return false
	}
	if sourceIdentRe.MatchString(t) {
		// One lowercase or uppercase letter run of a dozen letters or more
		// is a passphrase, not an identifier; code identifiers are short or
		// carry a camelCase transition or an underscore.
		return len(t) < 12 || strings.ContainsRune(t, '_') || camelTransitionRe.MatchString(t)
	}
	return memberChainRe.MatchString(t)
}

// credentialMatches returns the [start,end) offsets of every credential
// assignment whose value is a literal secret candidate. The span covers
// name, separator and value, as for the other redaction rules, except under a
// quoted key whose value has the same quotes ({"password":"…"}), where it
// covers the value inside its quotes only.
func credentialMatches(text string) [][2]int {
	var out [][2]int
	covered := 0 // end of the last consumed span; names inside it are part of it
	depthPos, curly, square := 0, 0, 0
	var inJSON jsonStringTracker
	for _, m := range credentialNameRe.FindAllStringSubmatchIndex(text, -1) {
		if m[2] < covered {
			continue
		}
		name := text[m[2]:m[3]]
		kind := classifyCredentialName(name)
		if kind == nameNone {
			continue
		}
		// Brackets still open at the name, on its line: a } or ] in an
		// unquoted value ends it only when it closes one of these.
		for ; depthPos < m[2]; depthPos++ {
			switch text[depthPos] {
			case '\n':
				curly, square = 0, 0
			case '{':
				curly++
			case '[':
				square++
			case '}':
				if curly > 0 {
					curly--
				}
			case ']':
				if square > 0 {
					square--
				}
			}
		}
		json := inJSON.at(text, m[2])
		val, end, quoted, complete := parseCredentialValue(text, m[1], curly, square, false, json)
		if !credentialValueOK(val, quoted, passwordNameRe.MatchString(name)) {
			if referenceValue(val, quoted) {
				covered = end // the reference is skipped whole, never redacted from inside
			}
			continue
		}
		if kind == nameLoose && !looksCredentialShape(val) {
			continue
		}
		if !complete {
			_, end, _, _ = parseCredentialValue(text, m[1], curly, square, true, json)
		}
		covered = end
		start := m[2]
		if quoted && m[3] < len(text) && (text[m[3]] == '"' || text[m[3]] == '\'') && text[m[1]] == text[m[3]] && end-1 > m[1] {
			// A quoted key with a value in the same quotes (JSON, JS and
			// Python dicts): only the value inside its quotes is replaced, so
			// the key and the document structure stay intact.
			start, end = m[1]+1, end-1
		} else if json && strings.HasPrefix(text[m[3]:], `\"`) {
			// The same for JSON encoded inside a JSON string: the value inside
			// its escaped quotes ({\"password\":\"…\"}), or an unquoted value
			// ({\"password\":…,\"next\":…}), is replaced on its own.
			switch {
			case quoted && strings.HasPrefix(text[m[1]:], `\"`) && strings.HasSuffix(text[:end], `\"`) && end-2 > m[1]+2:
				start, end = m[1]+2, end-2
			case !quoted && end > m[1]:
				start = m[1]
			}
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// redactCredentials replaces every credential-named assignment found by
// credentialMatches (matches are ordered and never overlap).
func redactCredentials(text string) string {
	ms := credentialMatches(text)
	if len(ms) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(text[last:m[0]])
		b.WriteString("[REDACTED]")
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
