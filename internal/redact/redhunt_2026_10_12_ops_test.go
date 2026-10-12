package redact

import (
	"strings"
	"testing"
)

const ops12Secret = "Zk3qP9vX2mN7bR4tY8wL1cH6"

func ops12AssertRedacted(t *testing.T, cases []string) {
	t.Helper()
	for _, c := range cases {
		if out := RedactSecrets(c); strings.Contains(out, "Zk3qP9vX2mN7") {
			t.Errorf("secret leaked: %q -> %q", c, out)
		}
	}
}

// The contextual key/value matcher lists api_key, access_token,
// refresh_token, client_secret, password … but not the bare key names
// `token`, `secret`, `passphrase`, `pwd`, nor the `_PASS`/`_PWD` env-var
// suffixes that sensitiveName itself treats as secret-bearing. These are
// among the most common credential shapes in tool output: `{"token": …}`
// API responses, `?token=` query strings, `"secret":` config, ADO/ODBC
// `Pwd=`, `DB_PASS=` / `MYSQL_PWD=` env lines.
func TestRED_RedactMissesBareTokenSecretAndPassKeys(t *testing.T) {
	ops12AssertRedacted(t, []string{
		`{"token": "` + ops12Secret + `"}`,
		`"secret": "` + ops12Secret + `"`,
		`https://api.example.com/v1/items?token=` + ops12Secret + `&page=2`,
		`passphrase=` + ops12Secret,
		`Server=db;User Id=sa;Pwd=` + ops12Secret + `;`,
		`DB_PASS=` + ops12Secret,
		`MYSQL_PWD=` + ops12Secret + ` mysql -u root`,
		`export TOKEN=` + ops12Secret,
	})
}

// The key/value matcher requires the value to follow `:` or `=` directly
// (optional spaces). Other ubiquitous separators leak the whole value:
// PHP/Ruby hash arrows (`'password' => '…'`), whitespace-separated CLI
// flags and .netrc entries (`--password …`, `password …`), and XML
// elements (`<password>…</password>`, Maven settings.xml / .NET config).
func TestRED_RedactMissesArrowFlagNetrcAndXMLSeparators(t *testing.T) {
	ops12AssertRedacted(t, []string{
		`'password' => '` + ops12Secret + `',`,
		`:api_key => "` + ops12Secret + `"`,
		`mysql -u root --password ` + ops12Secret,
		`machine api.example.com login bob password ` + ops12Secret,
		`<password>` + ops12Secret + `</password>`,
	})
}

// Authorization headers are redacted only for the Bearer and Basic
// schemes. The `token` scheme (GitHub/Gitea/Django REST: `Authorization:
// token …`) and other schemes leave the credential in clear.
func TestRED_RedactMissesNonBearerAuthorizationSchemes(t *testing.T) {
	ops12AssertRedacted(t, []string{
		`Authorization: token ` + ops12Secret,
		`Authorization: Token ` + ops12Secret,
		`"Authorization": "Token ` + ops12Secret + `"`,
	})
}

// Docker's ~/.docker/config.json stores registry credentials as
// "auth": base64(user:password). The Basic-credential decoder already
// recognises exactly this shape, but only after an Authorization header
// name, so `cat ~/.docker/config.json` output keeps the registry password.
func TestRED_RedactMissesDockerConfigAuth(t *testing.T) {
	in := `{"auths":{"ghcr.io":{"auth":"Ym9iOlprM3FQOXZYMm1ON2JSNHRZOHdMMWNINg=="}}}`
	if out := RedactSecrets(in); strings.Contains(out, "Ym9iOlprM3FQ") {
		t.Fatalf("docker registry auth leaked: %q", out)
	}
}
