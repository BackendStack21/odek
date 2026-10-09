package redact

import (
	"strings"
	"testing"
)

// A 20+ char password whose first 20 chars contain a special character must
// still be redacted.
func TestRED_YAMLPasswordWithSpecialCharLeaks(t *testing.T) {
	secret := "Sup3r$ecretPassw0rd1234"
	in := "password: " + secret
	out := RedactSecrets(in)
	if strings.Contains(out, "Passw0rd1234") || strings.Contains(out, "Sup3r") {
		t.Fatalf("password with special char leaked: %q", out)
	}
}

// The value match must not stop at the first special character and leave the
// tail of the secret behind.
func TestRED_PasswordTailAfterSpecialCharLeaks(t *testing.T) {
	in := "password=Zx9qLm2RvT7wPk4nYb8sH!dJ3QrT"
	out := RedactSecrets(in)
	if strings.Contains(out, "dJ3QrT") {
		t.Fatalf("tail of password leaked after special char: %q", out)
	}
}
