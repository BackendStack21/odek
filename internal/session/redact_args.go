package session

import (
	"encoding/json"
	"regexp"
	"strings"

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
		if out, err := json.Marshal(redactJSONValue(v)); err == nil {
			return string(out)
		}
	}
	red := redact.RedactSecrets(args)
	if json.Valid([]byte(red)) {
		return red
	}
	return `{"redacted":true}`
}

func redactJSONValue(v any) any {
	return redactJSONValueKeyed("", v)
}

// credentialArgKey matches object keys that name a credential outright
// (header names, login fields, cookies). Their values are replaced whatever
// the value looks like, since the redactor's key-context rules only fire when
// key and value are adjacent and long enough.
var credentialArgKey = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:x[_-]?api[_-]?key|api[_-]?key|api[_-]?secret|apikey|authorization|proxy[_-]?authorization|password|passwd|passphrase|secret|client[_-]?secret|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|id[_-]?token|session[_-]?token|private[_-]?key|secret[_-]?key|cookie|set[_-]?cookie|credentials?)(?:$|[^a-z0-9])`)

// redactJSONValueKeyed redacts v. key is the object key v sits under (empty at
// the root or inside an array of unkeyed values): string leaves are scanned
// together with it, because the redactor's key-context rules need the key
// adjacent to the value.
func redactJSONValueKeyed(key string, v any) any {
	switch t := v.(type) {
	case string:
		return redactKeyedString(key, t)
	case []any:
		for i := range t {
			t[i] = redactJSONValueKeyed(key, t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[redact.RedactSecrets(k)] = redactJSONValueKeyed(k, val)
		}
		return out
	default:
		return v
	}
}

func redactKeyedString(key, val string) string {
	red := redact.RedactSecrets(val)
	if key == "" || val == "" {
		return red
	}
	if credentialArgKey.MatchString(key) {
		return "[REDACTED]"
	}
	kb, err1 := json.Marshal(key)
	vb, err2 := json.Marshal(val)
	if err1 != nil || err2 != nil {
		return red
	}
	pair := string(kb) + ": " + string(vb)
	if redact.RedactSecrets(pair) != pair {
		return "[REDACTED]"
	}
	return red
}
