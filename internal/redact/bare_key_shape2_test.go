package redact

import "testing"

// Single unseparated alphanumeric runs (lowercase+digits, UPPER+digits) and
// quoted JSON/YAML forms are credentials too.
func TestBareKeyRulesRedactAlnumRunsAndQuotedForms(t *testing.T) {
	for _, in := range []string{
		"token=k3j5h7g9f1d3s5a7q9w1e3r5",
		"TOKEN=K3J5H7G9F1D3S5A7Q9W1E3R5",
		`{"token": "k3j5h7g9f1d3s5a7q9w1e3r5"}`,
		`"secret": "K3J5H7G9F1D3S5A7Q9W1E3R5X"`,
		"secret: k3j5h7g9f1d3s5a7q9w1e3r5",
		`token: 'k3j5h7g9f1d3s5a7q9w1e3r5'`,
		"MY_PASS=k3j5h7g9f1d3s5a7q9w1e3r5",
		"token=/aB3dEf7hIj9kLmN0pQrStUvWx",
	} {
		if out := RedactSecrets(in); out == in {
			t.Errorf("leaked: %q", in)
		}
	}
}

// Path-shaped values with word-like segments stay visible, and identifiers
// with separators stay visible. UUIDs and other dash-separated groups are
// deliberately not matched by the bare-key rules.
func TestBareKeyRulesKeepPathsAndSeparatedNames(t *testing.T) {
	for _, in := range []string{
		"secret=Users/Alice/Project2/keysfolder",
		"token=Home/Bob/Documents2/readmefile",
		"secret: access_token_v2_for_service",
		`"token": "access_token_v2_for_service"`,
		"token = self.oauth2_client.access_token_v2",
		"token: 123e4567-e89b-12d3-a456-426614174000",
		"token=abcdefghijklmnopqrstuvwxyz",
		"token=1234567890123456789012345",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}
