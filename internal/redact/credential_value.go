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
var credentialNameRe = regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(?:token|secret|passw(?:or)?d|credential|api[_-]?key|access[_-]?key|private[_-]?key)[A-Za-z0-9_-]*)["'\x60]?\s*(?:=>|[:=])\s*`)

// keyedNameRe matches the names whose value is always treated as a
// credential, wherever the name begins: it ends with one of these words.
var keyedNameRe = regexp.MustCompile(`(?i)(?:api[_-]?key|api[_-]?secret|auth[_-]?token|access[_-]?token|refresh[_-]?token|bearer[_-]?token|client[_-]?secret|private[_-]?key|secret[_-]?key|secret[_-]?access[_-]?key|session[_-]?token|security[_-]?token|password|passwd)$`)

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
	if strings.HasPrefix(t, "${") && strings.HasSuffix(t, "}") ||
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

// parseCredentialValue reads the value starting at pos: a quoted literal up
// to its closing quote on the same line (a backslash-escaped quote does not
// close it), or an unquoted run to the next whitespace, cut at `,"` (the
// start of the next JSON member) and at a closing } or ]. An opening quote
// without a closing one starts an unquoted run. It returns the value to
// judge (without quotes, at most maxCredentialValueCheck bytes), the end
// offset of the whole token, and whether it was a closed quoted literal.
func parseCredentialValue(text string, pos int) (val string, end int, quoted bool) {
	if pos >= len(text) {
		return "", pos, false
	}
	if c := text[pos]; c == '"' || c == '\'' || c == '`' {
		rest := text[pos+1:]
		limit := len(rest)
		if limit > maxCredentialValueCheck {
			limit = maxCredentialValueCheck
		}
		if nl := strings.IndexByte(rest[:limit], '\n'); nl >= 0 {
			limit = nl
		}
		for i := 0; i < limit; i++ {
			if rest[i] == '\\' {
				i++
			} else if rest[i] == c {
				return rest[:i], pos + 1 + i + 1, true
			}
		}
		pos++
	}
	end = pos
	for end < len(text) {
		switch text[end] {
		case ' ', '\t', '\n', '\r', '\f', '\v', '}', ']':
			return clipCredentialValue(text[pos:end]), end, false
		case ',':
			if end+1 < len(text) && text[end+1] == '"' {
				return clipCredentialValue(text[pos:end]), end, false
			}
		}
		end++
	}
	return clipCredentialValue(text[pos:]), end, false
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
		return !quotedReference(v)
	}
	return !codeExpression(v)
}

// credentialMatches returns the [start,end) offsets of every credential
// assignment whose value is a literal secret candidate. The span covers
// name, separator and value, as for the other redaction rules.
func credentialMatches(text string) [][2]int {
	var out [][2]int
	covered := 0 // end of the last match; names inside its value are part of it
	for _, m := range credentialNameRe.FindAllStringSubmatchIndex(text, -1) {
		if m[2] < covered {
			continue
		}
		name := text[m[2]:m[3]]
		kind := classifyCredentialName(name)
		if kind == nameNone {
			continue
		}
		val, end, quoted := parseCredentialValue(text, m[1])
		if !credentialValueOK(val, quoted, passwordNameRe.MatchString(name)) {
			continue
		}
		if kind == nameLoose && !looksCredentialShape(val) {
			continue
		}
		covered = end
		out = append(out, [2]int{m[2], end})
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
