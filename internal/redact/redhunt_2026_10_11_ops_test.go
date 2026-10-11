package redact

import (
	"strings"
	"testing"
)

// AWS STS / `aws configure export-credentials` / ~/.aws/cli/cache JSON and
// YAML-style env blocks carry the secret access key and session token under
// names no pattern recognises (SecretAccessKey, SessionToken,
// AWS_SECRET_ACCESS_KEY: ...). Only the non-secret AccessKeyId is redacted.
func TestRED_AWSSecretAccessKeyJSONAndYAML(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	const sessionTok = "FwoGZXIvYXdzEJr//////////wEaDOexampletokenvalue1234567890abcdef"
	inputs := []string{
		`{"Credentials": {"AccessKeyId": "ASIAIOSFODNN7EXAMPLE", "SecretAccessKey": "` + secret + `", "SessionToken": "` + sessionTok + `"}}`,
		"env:\n  AWS_SECRET_ACCESS_KEY: " + secret + "\n  AWS_SESSION_TOKEN: " + sessionTok + "\n",
	}
	for _, in := range inputs {
		out := RedactSecrets(in)
		if strings.Contains(out, secret) {
			t.Errorf("AWS secret access key leaked:\n in: %s\nout: %s", in, out)
		}
		if strings.Contains(out, sessionTok) {
			t.Errorf("AWS session token leaked:\n in: %s\nout: %s", in, out)
		}
	}
}

// The AWS key names must not redact prose that mentions them without a value.
func TestRED_AWSSecretAccessKeyProseUntouched(t *testing.T) {
	for _, in := range []string{
		"Set SecretAccessKey and SessionToken from the STS response before retrying.",
		"the session token expired; run aws sso login again",
		"AWS_SESSION_TOKEN: unset",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("prose over-redacted:\n in: %s\nout: %s", in, out)
		}
	}
}
