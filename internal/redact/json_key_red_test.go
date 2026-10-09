package redact

import (
	"strings"
	"testing"
)

// JSON writes the key with a closing quote before the colon; the value must
// still be redacted.
func TestRED_JSONQuotedKeyNotRedacted(t *testing.T) {
	secret := "Zx9qLm2RvT7wPk4nYb8sHd3J"
	in := `{"password": "` + secret + `"}`
	out := RedactSecrets(in)
	if strings.Contains(out, secret) {
		t.Fatalf("JSON-quoted password value leaked: %q", out)
	}
}
