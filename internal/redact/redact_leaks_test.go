package redact

import (
	"strings"
	"testing"
)

// PGP armored private key blocks (gpg --export-secret-keys output) passed
// fully unredacted: the PEM alternation covers RSA/EC/OPENSSH/DSA/ED25519/
// ENCRYPTED but not "PGP PRIVATE KEY BLOCK".
func TestRedactSecrets_PGPPrivateKeyBlock(t *testing.T) {
	pgp := `-----BEGIN PGP PRIVATE KEY BLOCK-----
lQVYBFJ4cAEBDADGW8P5zZU0exampleexampleexampleexampleexample
-----END PGP PRIVATE KEY BLOCK-----`
	if out := RedactSecrets(pgp); strings.Contains(out, "lQVYBFJ4cAE") {
		t.Fatal("PGP private key block survived redaction")
	}
}

// Modern Slack app tokens (xapp-1-<A-id>-<install>-<secret>) are not covered
// by the xox[abpos]- pattern and survived bare.
func TestRedactSecrets_SlackAppToken(t *testing.T) {
	tok := "xapp-1-A012B3C4D5E-1234567890123-0123456789abcdef0123456789abcdef"
	if out := RedactSecrets("configured token " + tok + " for the workspace"); strings.Contains(out, "0123456789abcdef") {
		t.Fatal("Slack xapp-1 app token survived redaction")
	}
}
