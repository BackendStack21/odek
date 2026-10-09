package redact

import (
	"strings"
	"testing"
)

// An unquoted credential value runs to the next whitespace. Stopping at a
// comma, semicolon or quote left the tail of a password in clear text.
func TestRED_UnquotedCredentialValueRunsToWhitespace(t *testing.T) {
	cases := []string{
		"password: Zx9qLm2RvT7wPk4nYb8s,Hd3JxQ9rT",
		"password: Zx9qLm2RvT7wPk4nYb8s;Hd3JxQ9rT",
		"password: Zx9qLm2RvT7wPk4nYb8s'Hd3JxQ9rT",
		"password: Zx9qLm2RvT7wPk4nYb8s\"Hd3JxQ9rT",
		"Password=Zx9qLm2RvT7wPk4nYb8sHd3J;User=bob",
	}
	for _, in := range cases {
		got := RedactSecrets(in)
		if strings.Contains(got, "Hd3J") {
			t.Errorf("%q -> %q: tail of the credential leaked", in, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("%q -> %q: nothing redacted", in, got)
		}
	}
	// Whitespace still ends the value, so the rest of a line survives.
	got := RedactSecrets("password: Zx9qLm2RvT7wPk4nYb8sHd3J user: bob")
	if !strings.HasSuffix(got, " user: bob") {
		t.Fatalf("text after whitespace must survive: %q", got)
	}
}
