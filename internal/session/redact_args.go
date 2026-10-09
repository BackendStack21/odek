package session

import (
	"encoding/json"
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
	switch t := v.(type) {
	case string:
		return redact.RedactSecrets(t)
	case []any:
		for i := range t {
			t[i] = redactJSONValue(t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[redact.RedactSecrets(k)] = redactJSONValue(val)
		}
		return out
	default:
		return v
	}
}
