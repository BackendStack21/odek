package redact

import (
	"strings"
	"testing"
)

// cfghunt13Secret is a generated-looking credential (mixed case + digits, 24
// chars) — the shape every value rule in this package accepts.
const cfghunt13Secret = "Xk9vQ2mL7pR4tY8wZ3nB6cF1"

func cfghunt13ExpectRedacted(t *testing.T, inputs []string) {
	t.Helper()
	for _, in := range inputs {
		got := RedactSecrets(in)
		if strings.Contains(got, cfghunt13Secret[:12]) || strings.Contains(got, cfghunt13Secret[12:]) {
			t.Errorf("secret leaked through redaction:\n  in:  %q\n  out: %q", in, got)
		}
		if !HasSecrets(in) {
			t.Errorf("HasSecrets(%q) = false, want true", in)
		}
	}
}

func cfghunt13ExpectUnchanged(t *testing.T, inputs []string) {
	t.Helper()
	for _, in := range inputs {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction:\n  in:  %q\n  out: %q", in, got)
		}
	}
}

// The database-URL rule lists redis among the credential-bearing schemes but
// requires a non-empty username before the ':'. The standard Redis URL form
// carries only a password (redis://:PASSWORD@host, the form REDIS_URL /
// CELERY_BROKER_URL / Heroku and most client docs use), and the TLS scheme
// rediss:// is not listed at all, so both leak the password verbatim.
func TestRED_RedisURLPasswordWithoutUsernameLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"REDIS_URL=redis://:" + cfghunt13Secret + "@cache.internal:6379/0",
		"CELERY_BROKER_URL=rediss://:" + cfghunt13Secret + "@cache.internal:6380/0",
		"rediss://default:" + cfghunt13Secret + "@cache.internal:6380",
	})
	cfghunt13ExpectUnchanged(t, []string{
		"REDIS_URL=redis://cache.internal:6379/0",
		"redis://localhost:6379",
	})
}

// SQLAlchemy / Django style driver-qualified schemes (postgresql+psycopg2://,
// mysql+pymysql://, mssql+pyodbc://) are the usual DATABASE_URL form in
// Python projects, but the scheme alternation does not allow a "+driver"
// suffix, so the embedded password is not redacted.
func TestRED_DriverQualifiedDatabaseURLLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"DATABASE_URL=postgresql+psycopg2://app:" + cfghunt13Secret + "@db.internal/app",
		"SQLALCHEMY_DATABASE_URI=mysql+pymysql://app:" + cfghunt13Secret + "@db.internal/app",
		"mssql+pyodbc://sa:" + cfghunt13Secret + "@db.internal/app?driver=ODBC+Driver+18",
	})
}

// The bare token/secret rule judges the value with looksCredentialShape,
// which rejects any value containing a backslash. In JSON-encoded output
// (kubectl get configmap -o json, gh api file contents, MCP results) a
// multi-line value is one string with "\n" escapes, so the credential is
// followed directly by "\n…" and the whole value is rejected: the secret
// leaks. Env-style names (API_KEY=…) in the same position are redacted.
func TestRED_BareSecretKeyBeforeJSONEscapedNewlineLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		`{"data":{"app.yaml":"token: ` + cfghunt13Secret + `\nlog_level: debug\n"}}`,
		`{"content":"secret=` + cfghunt13Secret + `\nDEBUG=1"}`,
	})
}

// curl -u / --user USER:PASSWORD puts an HTTP Basic credential on the command
// line; the command is persisted in session tool-call arguments, audit
// records and logs. The CLI flag rule covers --password/--token/... but not
// curl's credential flag, so the password is kept.
func TestRED_CurlUserFlagCredentialLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"curl -u admin:" + cfghunt13Secret + " https://api.example.com/v1/items",
		"curl --user admin:" + cfghunt13Secret + " https://api.example.com/v1/items",
	})
	cfghunt13ExpectUnchanged(t, []string{
		"curl -u admin https://api.example.com/v1/items",
		"curl -s https://api.example.com/v1/items",
	})
}

func TestCurlUserFlagCorpus(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"curl --user=admin:" + cfghunt13Secret + " https://x.example",
		`curl -u "admin:` + cfghunt13Secret + `" https://x.example`,
		"curl --proxy-user px:" + cfghunt13Secret + " https://x.example",
	})
	if got := RedactSecrets("curl -u admin:hunter2pw https://x.example"); strings.Contains(got, "hunter2pw") {
		t.Errorf("short password leaked: %q", got)
	}
	cfghunt13ExpectUnchanged(t, []string{
		"docker run -u 1000:1000 alpine",
		"docker run -u www-data:www-data alpine",
		"docker run --user appuser:docker alpine",
		"curl -u admin:$CURL_PASSWORD https://x.example",
		`curl -u "admin:${CURL_PASSWORD}" https://x.example`,
		"git config user.name x && mkdir-u a:b",
		"pip install -U pip",
	})
}

// Session cookies are bearer credentials: a Cookie / Set-Cookie header line in
// curl -v output or an http_request result replays the session. No rule
// covers cookie headers, so the session value is persisted verbatim.
func TestRED_CookieHeaderSessionValueLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"Cookie: sessionid=" + cfghunt13Secret,
		"< Set-Cookie: session=" + cfghunt13Secret + "; Path=/; HttpOnly",
		`curl -H "Cookie: sid=` + cfghunt13Secret + `" https://app.example.com/`,
	})
}

func TestCookieHeaderCorpus(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"cookie: theme=dark; sid=" + cfghunt13Secret,
		`{"Cookie": "lang=en; connect.sid=s%3A` + cfghunt13Secret + `.sig"}`,
		"Set-Cookie: sid=" + cfghunt13Secret + "; Expires=Wed, 21 Oct 2026 07:28:00 GMT; Secure",
	})
	cfghunt13ExpectUnchanged(t, []string{
		"Cookie: theme=dark; lang=en-US; consent=yes",
		"Set-Cookie: Path=/; Domain=sub1.example.com; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly",
		"Cookie: sessionid=<your-session-id-here>",
		"Cookie: sid=$SESSION_COOKIE_VALUE_HERE",
		"the cookie: a small piece of text a site stores in the browser",
		"cookie jar: /home/user/.cache/cookies.txt",
	})
}

// Incoming-webhook URLs are bearer credentials (anyone holding the URL can
// post as the integration); secret scanners treat them as such. They are not
// redacted.
func TestRED_ChatWebhookURLLeaks(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"SLACK_WEBHOOK_URL=https://hooks.slack" + ".com/services/T01ABCDEF/B02GHIJKL/" + cfghunt13Secret,
		"https://discord.com/api/webhooks/112233445566778899/" + cfghunt13Secret + cfghunt13Secret,
	})
}
