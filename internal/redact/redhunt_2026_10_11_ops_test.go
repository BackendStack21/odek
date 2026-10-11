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

// HTTP Basic credentials are base64(user:password). curl -v, HTTP dumps
// and request logs print them as "Authorization: Basic <b64>"; only the
// Bearer form is covered.
func TestRED_AuthorizationBasicNotRedacted(t *testing.T) {
	// base64("username:supersecretpassword123")
	const cred = "dXNlcm5hbWU6c3VwZXJzZWNyZXRwYXNzd29yZDEyMw=="
	for _, in := range []string{
		"> GET /api HTTP/1.1\n> Authorization: Basic " + cred + "\n",
		"Proxy-Authorization: basic " + cred,
		`{"headers": {"Authorization": "Basic ` + cred + `"}}`,
		// base64("a:b") — short credentials are still credentials.
		"Authorization: Basic YTpi",
	} {
		out := RedactSecrets(in)
		if strings.Contains(out, cred) || strings.Contains(out, "YTpi") {
			t.Errorf("Basic auth credential leaked: %q", out)
		}
		if !HasSecrets(in) || CountSecrets(in) == 0 {
			t.Errorf("HasSecrets/CountSecrets miss the Basic credential in %q", in)
		}
	}
}

// The word "Basic" in prose, or a Basic header whose token is not a
// base64 user:password pair, must stay readable.
func TestRED_AuthorizationBasicProseUntouched(t *testing.T) {
	for _, in := range []string{
		"Basic usage: run odek with a goal.",
		"The server requires Authorization: Basic authentication over TLS.",
		"Authorization: Basic credentials are rejected by this endpoint",
		"See the Basic Auth section; Authorization headers are documented there.",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("prose over-redacted:\n in: %s\nout: %s", in, out)
		}
		if HasSecrets(in) {
			t.Errorf("HasSecrets true for prose %q", in)
		}
	}
}

func TestRED_BasicCredentialShape(t *testing.T) {
	cases := map[string]bool{
		"dXNlcjpwYXNz":     true,  // user:pass
		"YTpiYw":           true,  // a:bc, unpadded
		"dXNlcnBhc3M=":     false, // userpass, no colon
		"AAE6AgM=":         false, // control bytes around ':'
		"_-_-":             false, // decodes to non-UTF-8 bytes
		"!!notbase64!!":    false,
		"w7xzZXI6cMOkc3M=": true, // üser:päss
	}
	for tok, want := range cases {
		if got := basicCredential(tok); got != want {
			t.Errorf("basicCredential(%q) = %v, want %v", tok, got, want)
		}
	}
}
