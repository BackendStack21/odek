package redact

import (
	"strings"
	"testing"
)

// The secret body every leak case carries; the probe checks for its
// distinctive middle chunks so a special character inserted into the value
// cannot hide a leak from the assertion.
func ops12bLeaked(out string) bool {
	for _, frag := range []string{"Zk3q", "P9vX", "2mN7", "bR4t", "Y8wL", "1cH6"} {
		if strings.Contains(out, frag) {
			return true
		}
	}
	return false
}

// The bare-key rules (token, secret, passphrase, _PASS, _PWD, --token=)
// accept a value only when every byte is in [A-Za-z0-9+/=_~-]. Real
// passwords and client secrets routinely carry !, @, #, $, *, % or '.', and
// one such character is enough for the whole value to pass through in clear.
// This is the class the earlier "generic credential value class excludes
// common password characters" fix closed for the keyed matcher; the new
// guarded rules reintroduced it.
func TestRED_RedactBareKeyRulesLeakPasswordsWithSpecialCharacters(t *testing.T) {
	for _, in := range []string{
		`{"token": "Zk3q!P9vX@2mN7bR4t#Y8wL1cH6"}`,
		`{"secret": "Zk3q!P9vX2mN7bR4tY8wL1cH6"}`,
		`DB_PASS=Zk3q!P9vX@2mN7bR4t#Y8wL1cH6`,
		`REDIS_PASS=Zk3q*P9vX2mN7bR4tY8wL1cH6`,
		`MYSQL_PWD='Zk3q$P9vX2mN7bR4tY8wL1cH6'`,
		`passphrase: Zk3q*P9vX2mN7bR4tY8wL1cH6`,
		`--token=Zk3q!P9vX2mN7bR4tY8wL1cH6`,
	} {
		if out := RedactSecrets(in); ops12bLeaked(out) {
			t.Errorf("secret leaked: %q -> %q", in, out)
		}
	}
}

// The env-var matcher requires at least three characters BEFORE the secret
// word ([A-Z_]{3,}(?:API_KEY|TOKEN|SECRET|PASSWORD…)), and the keyed matcher
// requires the separator right after the key. An env/config name whose
// secret word is the leading segment matches neither: Rails'
// SECRET_KEY_BASE (env and credentials.yml), API_KEY_PROD, TOKEN_GITHUB,
// PASSWORD_ADMIN, ACCESS_TOKEN_V2. sensitiveName — the package's own notion
// of a secret-bearing name — classifies every one of these as sensitive.
func TestRED_RedactLeaksEnvNamesWithLeadingSecretSegment(t *testing.T) {
	for _, in := range []string{
		`SECRET_KEY_BASE=Zk3qP9vX2mN7bR4tY8wL1cH6Zk3qP9vX2mN7bR4tY8wL1cH6`,
		`  secret_key_base: Zk3qP9vX2mN7bR4tY8wL1cH6Zk3qP9vX2mN7bR4tY8wL1cH6`,
		`API_KEY_PROD=Zk3qP9vX2mN7bR4tY8wL1cH6`,
		`TOKEN_GITHUB=Zk3qP9vX2mN7bR4tY8wL1cH6`,
		`PASSWORD_ADMIN=Zk3q!P9vX2mN7bR4tY8wL1cH6`,
		`ACCESS_TOKEN_V2=Zk3q!P9vX2mN7bR4tY8wL1cH6`,
	} {
		name, _, _ := strings.Cut(strings.TrimSpace(in), "=")
		name, _, _ = strings.Cut(name, ":")
		if !sensitiveName(name) {
			t.Fatalf("precondition: sensitiveName(%q) = false", name)
		}
		if out := RedactSecrets(in); ops12bLeaked(out) {
			t.Errorf("secret leaked: %q -> %q", in, out)
		}
	}
}

// RFC 6750 b64token allows '~' (Azure AD and other issuers emit it). The
// Authorization pattern's token class omits it, so the match needs 20
// characters before the first '~' and a token with an early '~' is never
// redacted at all.
func TestRED_RedactBearerTokenWithTildeLeaks(t *testing.T) {
	for _, in := range []string{
		`Authorization: Bearer Zk3q~P9vX2mN7bR4tY8wL1cH6`,
		`"Authorization": "Bearer Zk3q~P9vX2mN7bR4tY8wL1cH6"`,
	} {
		if out := RedactSecrets(in); ops12bLeaked(out) {
			t.Errorf("bearer token leaked: %q -> %q", in, out)
		}
	}
}

// Tool results are redacted before the model sees them, so redacting
// ordinary source code corrupts read_file output and every patch built from
// it. The keyed matcher (password, api_key, access_token, refresh_token …)
// takes any 20+ non-space run after the separator, so member-access chains,
// environment lookups and function calls are replaced — although the
// guarded bare-key rules deliberately keep exactly these shapes
// (`token = self.oauth2_client.access_token_v2` stays visible).
func TestRED_RedactCorruptsSourceCodeAfterKeyedNames(t *testing.T) {
	for _, in := range []string{
		"    password: user.hashedPasswordFromDatabase,",
		"apiKey: process.env.OPENAI_API_KEY_VALUE,",
		"api_key = os.environ['OPENAI_API_KEY']",
		"self.access_token = access_token_response['token']",
		"refresh_token=response.json()['refresh_token']",
		"password = hashlib.sha256(raw_pw).hexdigest()",
		"password: ${DATABASE_PASSWORD_FROM_VAULT}",
		// Minified JSON (curl output): a null/empty value lets the unquoted
		// alternative run on through the following fields.
		`{"api_key":null,"model":"gpt-4o","temperature":0.2,"max_tokens":1000}`,
		`{"user":"bob","password":"","host":"db.internal","port":5432}`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("source code over-redacted: %q -> %q", in, out)
		}
	}
}

// The credential env-var matcher is compiled case-insensitive, so the
// `[A-Z_]{3,}…TOKEN…` name pattern meant for UPPER_CASE environment
// variables matches any lower-case identifier containing token/secret/
// password, and its value class swallows a whole function call. Ordinary
// code such as a token-budget constant is erased from read_file output.
func TestRED_RedactEnvVarRuleCorruptsIdentifiersAndCalls(t *testing.T) {
	for _, in := range []string{
		"const MAX_TOKEN_COUNT = computeTokenBudgetFromConfig()",
		"max_token_count = self.compute_token_budget_value()",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("source code over-redacted: %q -> %q", in, out)
		}
	}
}

// Code expressions after credential-shaped names stay readable; real
// literals are replaced without touching the rest of the line or JSON.
func TestRED_RedactCredentialValueCheckKeepsCodeAndBoundsLiterals(t *testing.T) {
	unchanged := []string{
		`api_key = os.environ['OPENAI_API_KEY']`,
		`{"api_key":null,"model":"x"}`,
		`password: ${DB_PASSWORD}`,
		`const MAX_TOKEN_COUNT = computeTokenBudgetFromConfig()`,
		`password = getpass.getpass()`,
		`MaxToken = 12345678901234567890123`,
		`password: "short"`,
	}
	for _, in := range unchanged {
		if out := RedactSecrets(in); out != in {
			t.Errorf("code changed: %q -> %q", in, out)
		}
	}
	for _, in := range []string{
		`password=hunter2!@#Secret`,
		`SECRET_KEY_BASE=8f2a9c1d7e3b4a5f6c0d1e2f3a4b5c6d`,
		`{"api_key":"sk-Zk3qP9vX2mN7bR4tY8wL1cH6","model":"x"}`,
	} {
		out := RedactSecrets(in)
		if !strings.Contains(out, "[REDACTED]") || strings.Contains(out, "hunter2") {
			t.Errorf("not redacted: %q -> %q", in, out)
		}
	}
	if out := RedactSecrets(`{"api_key":"sk-Zk3qP9vX2mN7bR4tY8wL1cH6","model":"x"}`); !strings.HasSuffix(out, `,"model":"x"}`) {
		t.Errorf("rest of JSON damaged: %q", out)
	}
	if out := RedactSecrets(`{"user":"bob","password":"Zk3qP9vX!2mN7bR4tY8wL1cH6","host":"db"}`); !strings.HasSuffix(out, `,"host":"db"}`) {
		t.Errorf("rest of JSON damaged: %q", out)
	}
}
