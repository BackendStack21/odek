package redact

import "testing"

// Ordinary code, config and identifiers that merely contain a digit must
// survive the bare token/secret/pass/pwd rules.
func TestBareKeyRulesLeaveCodeAlone(t *testing.T) {
	for _, in := range []string{
		"token = self.oauth2_client.access_token_v2",
		"const secret = process.env.STRIPE_SECRET_KEY_V2;",
		"token: env.GITHUB_TOKEN_FOR_RELEASE2",
		"secret: app-tls-cert-2024-renewed",
		"secret = os.environ['DB_SECRET_2']",
		"--token $GITHUB_TOKEN_FOR_CI_2026",
		"apiToken => config.services.api2.token",
		"Secret: arn:aws:secretsmanager:us-east-1:123456789012:secret:db-creds",
		`"secret": "projects/my-proj-123/secrets/db-pass/versions/latest"`,
		"token: 2026-10-12T10:20:30.123456Z",
		"TOKEN=v1.2.3-release-candidate-2026",
		"Token: github.com/org/repo/v2/internal/token",
		"secret: internal/secrets/store_v2.go",
		"MY_PASS=build-2024-10-12-success",
		"token = ENV['GITHUB_TOKEN_NUMBER_2']",
		"secret: %SECRET_VALUE_NAME_2%",
		"secret = ${SECRET_VALUE_NAME_2}",
		"secret: some_resource_name_for_v2_deploy",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}

func TestBareKeyRulesStillRedactRealSecrets(t *testing.T) {
	for _, in := range []string{
		"token=Xy7Kq9Lm2Np4Rs6Tv8Wz0",
		`secret: "a8f5f167f44f4964e6c998dee827110c"`,
		"Token: Xy7Kq9Lm2Np4Rs6Tv8Wz0AbCd",
		"pwd=Xy7Kq9Lm2Np4Rs6Tv8Wz0",
		"secret = aGVsbG8gd29ybGQgc2VjcmV0+/abc123==",
	} {
		if out := RedactSecrets(in); out == in {
			t.Errorf("leaked: %q", in)
		}
	}
}
