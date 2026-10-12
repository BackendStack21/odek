package session

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/BackendStack21/odek/internal/redact"
)

// redactToolArguments redacts tool-call arguments without breaking their JSON
// framing: the redactor's unquoted-value match runs to the next whitespace, so
// applying it to the raw text can swallow a closing quote or brace and leave
// arguments that providers reject on replay. Valid JSON is redacted
// structurally (every string value and object key); anything else is redacted
// as text and, if that still is not valid JSON, replaced by a placeholder
// object with the original dropped.
func redactToolArguments(args string) string {
	if args == "" {
		return args
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil && !dec.More() {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(redactJSONValue(v)); err == nil {
			return strings.TrimSuffix(buf.String(), "\n")
		}
	}
	red := redact.RedactSecrets(args)
	if json.Valid([]byte(red)) {
		return red
	}
	return `{"redacted":true}`
}

func redactJSONValue(v any) any {
	return redactJSONValueKeyed(false, v)
}

// credentialWords are whole name segments that mark a key as a credential
// when they end the name (access_token, X-Api-Key, dbPassword, Set-Cookie).
var credentialWords = map[string]bool{
	"password": true, "passwd": true, "passphrase": true, "pwd": true, "pw": true,
	"secret": true, "secrets": true, "token": true, "apikey": true,
	"authorization": true, "cookie": true, "cookies": true,
	"credential": true, "credentials": true,
}

// keySegments splits a key into lower-case name segments on separators and
// camelCase boundaries.
func keySegments(key string) []string {
	var segs []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(key)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return segs
}

// isCredentialKey reports whether the key names a credential outright. Whole
// segments are compared, so token_count, max_tokens and tokenizer stay.
func isCredentialKey(key string) bool {
	segs := keySegments(key)
	n := len(segs)
	if n == 0 {
		return false
	}
	last := segs[n-1]
	if credentialWords[last] {
		return true
	}
	if last == "key" && n > 1 {
		switch segs[n-2] {
		case "api", "private", "secret", "access", "auth", "client", "signing":
			return true
		}
	}
	return false
}

// secretFlag matches an argv flag whose next element is a credential value.
var secretFlag = regexp.MustCompile(`(?i)^--?(?:password|passwd|passphrase|pwd|pass|token|secret|api[_-]?key|secret[_-]?key|private[_-]?key|access[_-]?key|client[_-]?secret|auth[_-]?token)$`)

// redactJSONValueKeyed redacts v. cred is true when an enclosing object key
// names a credential: every string leaf below it is replaced.
func redactJSONValueKeyed(cred bool, v any) any {
	switch t := v.(type) {
	case string:
		red := redact.RedactSecrets(t)
		if cred && t != "" && red == t {
			return "[REDACTED]"
		}
		return red
	case []any:
		for i := range t {
			t[i] = redactJSONValueKeyed(cred, t[i])
		}
		// argv-shaped arrays: the element after a secret-taking flag.
		for i := 0; i+1 < len(t); i++ {
			if s, ok := t[i].(string); ok && secretFlag.MatchString(s) {
				if next, ok := t[i+1].(string); ok && next != "" {
					t[i+1] = "[REDACTED]"
				}
			}
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[redact.RedactSecrets(k)] = redactJSONValueKeyed(cred || isCredentialKey(k), val)
		}
		return out
	default:
		return v
	}
}
