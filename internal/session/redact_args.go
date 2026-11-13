package session

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	if v, ok := decodeToolArguments(args); ok {
		if out, ok := encodeToolArguments(redactJSONValue(v)); ok {
			return out
		}
	}
	red := redact.RedactSecrets(args)
	if json.Valid([]byte(red)) {
		return red
	}
	return `{"redacted":true}`
}

// decodeToolArguments reads args the way the store does before it rewrites
// them: one JSON value, numbers kept as written. It stops at the end of that
// value, so text after it that starts with '}' or ']' is ignored.
func decodeToolArguments(args string) (any, bool) {
	var v any
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return v, true
}

// encodeToolArguments writes a decoded value the way the store writes
// tool-call arguments: compact, object keys sorted, no HTML escaping.
func encodeToolArguments(v any) (string, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}

// CanonicalToolArguments returns tool-call arguments in the form the store
// writes them when redaction changes nothing in them (redactToolArguments
// with no secret present): decoded, then encoded compact with object keys
// sorted and no HTML escaping. The store applies the same decode and encode,
// so the two cannot drift apart.
//
// ok is true only when that rewrite cannot change what any decode of the
// arguments into a Go value reads: args is exactly one valid JSON value, and
// no object in it has two keys that encoding/json would match to the same
// struct field (equal, or equal under its case folding: "path" and "PATH").
// With such a pair the original decodes last-wins in written order, while the
// rewrite keeps one key or reorders them, so a struct decode of the two
// forms can read different values. For every other input each struct field
// receives at most one key, holding the same decoded value in both forms.
// The canonical form is itself canonical: CanonicalToolArguments of it
// returns it unchanged with ok set.
//
// Arguments already in canonical form (every message a reload hands back)
// skip the scans of the text: when the rewrite reproduces args byte for
// byte, args is one valid value with nothing after it (the encoder writes
// nothing else) and repeats no key in any object (the rewrite of a decoded
// object writes each key once), so only keys differing in case remain to be
// checked, on the decoded value.
func CanonicalToolArguments(args string) (string, bool) {
	v, ok := decodeToolArguments(args)
	if !ok {
		return "", false
	}
	canon, ok := encodeToolArguments(v)
	if !ok {
		return "", false
	}
	if canon == args {
		if !decodedKeysFoldDistinct(v) {
			return "", false
		}
		return canon, true
	}
	if !json.Valid([]byte(args)) || !toolArgumentKeysDistinct(args) {
		return "", false
	}
	return canon, true
}

// decodedKeysFoldDistinct reports whether no object in the decoded value v
// holds two keys equal under jsonFoldName.
func decodedKeysFoldDistinct(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if len(t) > 1 {
			seen := make(map[string]struct{}, len(t))
			for k := range t {
				f := jsonFoldName(k)
				if _, dup := seen[f]; dup {
					return false
				}
				seen[f] = struct{}{}
			}
		}
		for _, e := range t {
			if !decodedKeysFoldDistinct(e) {
				return false
			}
		}
	case []any:
		for _, e := range t {
			if !decodedKeysFoldDistinct(e) {
				return false
			}
		}
	}
	return true
}

// toolArgumentKeysDistinct reports whether no object in the JSON text args
// holds two keys equal under encoding/json's field-name folding
// (jsonFoldName). Malformed text reports false.
func toolArgumentKeysDistinct(args string) bool {
	if !strings.Contains(args, "{") {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(args))
	// One frame per open container: keys is nil for an array; key is set
	// while the next token of an object is a key.
	type frame struct {
		keys map[string]struct{}
		key  bool
	}
	var stack []frame
	// valueDone marks the end of a value inside the innermost container.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].keys != nil {
			stack[n-1].key = true
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		if n := len(stack); n > 0 && stack[n-1].key {
			if k, ok := tok.(string); ok {
				f := jsonFoldName(k)
				if _, dup := stack[n-1].keys[f]; dup {
					return false
				}
				stack[n-1].keys[f] = struct{}{}
				stack[n-1].key = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, frame{keys: make(map[string]struct{}), key: true})
		case json.Delim('['):
			stack = append(stack, frame{})
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}

// jsonFoldName returns the key encoding/json matches struct field names by:
// ASCII letters upper-cased, every other rune replaced by the smallest rune
// of its simple case-folding orbit (so "K", "k" and the Kelvin sign agree).
// Two names fold alike exactly when strings.EqualFold holds for them.
func jsonFoldName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		for {
			r2 := unicode.SimpleFold(r)
			if r2 <= r {
				r = r2
				break
			}
			r = r2
		}
		b.WriteRune(r)
		i += n
	}
	return b.String()
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
