package redact

import (
	"testing"
)

// Ordinary prose and short or non-credential values must survive the
// broadened key/separator matching untouched.
func TestOps12RedactLeavesProseAlone(t *testing.T) {
	for _, in := range []string{
		"The token is rotated every week and the secret is stored in the vault.",
		"Pass the token to the next stage; password must be changed.",
		"Authorization: token authentication is described in the docs",
		"Authorization: Bearer short",
		"the password verification step requires a longer passphrase",
		"token: abc123",
		"compass=northnorthwestnorthnorthwest",
		"token_count: 1234567890123456789012",
		`{"auth": "enabled"}`,
		`{"auth": "bm90LWEtY3JlZGVudGlhbA=="}`,
		"<password>short</password>",
		"mysql --password",
		"password hashing",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}

func TestOps12RedactDockerAuthCounts(t *testing.T) {
	in := `{"auths":{"ghcr.io":{"auth":"Ym9iOlprM3FQOXZYMm1ON2JSNHRZOHdMMWNINg=="}}}`
	if !HasSecrets(in) || CountSecrets(in) != 1 {
		t.Fatalf("HasSecrets/CountSecrets disagree: %v %d", HasSecrets(in), CountSecrets(in))
	}
}
