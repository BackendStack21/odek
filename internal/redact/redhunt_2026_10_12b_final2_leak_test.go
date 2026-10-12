package redact

import (
	"strings"
	"testing"
)

// A letters-only secret followed by punctuation is a secret; identifier
// shapes that look like code are not.
func TestRED_LettersOnlySecretBeforePunctuation(t *testing.T) {
	for _, in := range []string{
		"password=correcthorsebatterystaple,",
		"api_key=abcdefghijklmnopqrstuvwxyz;",
		"PASSWORD=CORRECTHORSEBATTERYSTAPLE)",
	} {
		out := RedactSecrets(in)
		if strings.Contains(strings.ToLower(out), "correcthorse") || strings.Contains(out, "abcdefghijkl") {
			t.Errorf("leak: %q -> %q", in, out)
		}
	}
	for _, in := range []string{
		"Password: password,",
		"Password: cfg.Password,",
		"Password: *body.Password,",
		"Password: passwordFromVault,",
		"Password: my_password_var,",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}
