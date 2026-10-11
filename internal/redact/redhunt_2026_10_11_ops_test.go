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

// The Bearer pattern requires "Authorization:" immediately followed by
// optional whitespace, so the JSON/JS header-object form
// ("Authorization": "Bearer ...") used in fetch() code, HTTP tool
// arguments and MCP configs is never matched.
func TestRED_AuthorizationBearerJSONHeaderNotRedacted(t *testing.T) {
	const tok = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	for _, in := range []string{
		`fetch(url, {headers: {"Authorization": "Bearer ` + tok + `"}})`,
		`headers: {'Authorization': 'Bearer ` + tok + `'}`,
		"Authorization = `Bearer " + tok + "`",
	} {
		if out := RedactSecrets(in); strings.Contains(out, tok) {
			t.Errorf("bearer token in JSON header leaked: %q", out)
		}
	}
	// Prose naming the scheme stays readable.
	for _, in := range []string{
		`Set "Authorization": "Bearer <token>" on every request.`,
		"The Authorization header uses the Bearer scheme described in RFC 6750.",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("prose over-redacted:\n in: %s\nout: %s", in, out)
		}
	}
}

// OAuth token-endpoint responses carry opaque refresh tokens under
// "refresh_token"; the generic matcher lists access_token/auth_token but
// not refresh_token, so the long-lived credential is persisted verbatim.
func TestRED_OAuthRefreshTokenNotRedacted(t *testing.T) {
	const tok = "def50200a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8"
	for _, in := range []string{
		`{"token_type":"Bearer","expires_in":3600,"refresh_token":"` + tok + `"}`,
		"refreshToken: " + tok,
		"REFRESH_TOKEN=" + tok,
	} {
		if out := RedactSecrets(in); strings.Contains(out, tok) {
			t.Errorf("refresh token leaked: %q", out)
		}
	}
	// Docs text that names the field without a value stays readable.
	for _, in := range []string{
		"Store the refresh_token securely and rotate it on every use.",
		"The response includes a refresh_token field; see the OAuth guide.",
		`"refresh_token": "string"`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("prose over-redacted:\n in: %s\nout: %s", in, out)
		}
	}
}

// The known-value replacer is built from map iteration order. Go's
// strings.Replacer picks, at each position, the FIRST argument (not the
// longest) that matches, so when one registered secret is a prefix of
// another the shorter one can win and the tail of the longer secret is
// printed in clear. The outcome changes from process to process.
func TestRED_KnownValueReplacerPrefixLeaksTail(t *testing.T) {
	defer ResetSecrets()
	const short = "Pr0dPassw0rd"
	const long = "Pr0dPassw0rd-ROTATED-TAIL-Zq9x"
	for i := 0; i < 50; i++ {
		ResetSecrets()
		RegisterSecret(short)
		RegisterSecret(long)
		out := RedactSecrets("value=" + long)
		if strings.Contains(out, "ROTATED-TAIL-Zq9x") {
			t.Fatalf("iteration %d: tail of a registered secret leaked: %q", i, out)
		}
		if out != "value=[REDACTED]" {
			t.Fatalf("iteration %d: want one whole-secret replacement, got %q", i, out)
		}
		forms := currentForms()
		for j := 1; j < len(forms); j++ {
			if len(forms[j]) > len(forms[j-1]) {
				t.Fatalf("forms not ordered longest first: %q before %q", forms[j-1], forms[j])
			}
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
