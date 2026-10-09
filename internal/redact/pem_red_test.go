package redact

import (
	"strings"
	"testing"
)

// Legacy encrypted PEM blocks carry Proc-Type/DEK-Info headers containing
// dashes; the whole block must still be redacted.
func TestRED_LegacyEncryptedPEMNotRedacted(t *testing.T) {
	in := "-----BEGIN RSA PRIVATE KEY-----\n" +
		"Proc-Type: 4,ENCRYPTED\n" +
		"DEK-Info: AES-128-CBC,0123456789ABCDEF0123456789ABCDEF\n\n" +
		"MIIEowIBAAKCAQEAabcdefghijklmnopqrstuvwxyz0123456789\n" +
		"-----END RSA PRIVATE KEY-----\n"
	out := RedactSecrets(in)
	if strings.Contains(out, "MIIEowIBAAKCAQEA") || strings.Contains(out, "DEK-Info") {
		t.Fatalf("encrypted PEM body leaked: %q", out)
	}
}

func TestPEM_SurroundingTextAndPGPKept(t *testing.T) {
	in := "before\n-----BEGIN PGP PRIVATE KEY BLOCK-----\nComment: a-b\n\nlQOYBF1234567890\n-----END PGP PRIVATE KEY BLOCK-----\nafter"
	out := RedactSecrets(in)
	if strings.Contains(out, "lQOYBF") || !strings.HasPrefix(out, "before\n") || !strings.HasSuffix(out, "\nafter") {
		t.Fatalf("unexpected: %q", out)
	}
}
