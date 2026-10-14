package redact

import (
	"strings"
	"testing"
	"time"
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

func TestCurlScopedCredentialFlag(t *testing.T) {
	for in, want := range map[string]string{
		"curl -uadmin:Sup3rS3cretPw https://x.example":      "curl -uadmin:[REDACTED] https://x.example",
		"curl -u admin:letmeinplease https://x.example":     "curl -u admin:[REDACTED] https://x.example",
		"curl -sSLu admin:letmein https://x.example":        "curl -sSLu admin:[REDACTED] https://x.example",
		`curl -u "admin:letmeinplease" https://x.example`:   `curl -u "admin:[REDACTED]" https://x.example`,
		"curl --user admin:letmeinplease https://x.example": "curl --user admin:[REDACTED] https://x.example",
		"wget --user=a --password=letmeinplease https://x":  "wget --user=a --password=[REDACTED] https://x",
		"http --auth admin:letmeinplease GET https://x":     "http --auth admin:[REDACTED] GET https://x",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		"rsync -u host:/var/backups1/data",
		"ls -U dir:abc12345",
		"pip install -U pkg:v12345678",
		"sudo -u postgres:postgres1 psql",
		"docker run -u appuser:appgroup2 alpine",
		"curl -s https://x.example | sudo -u postgres:postgres1 psql",
		"curl -u admin:password https://x.example",
		"curl --user-agent foo:barbarbar1 https://x.example",
		"curl -u admin:$PASS https://x.example",
	})
}

func TestCommandScopeAcrossContinuationsAndQuotes(t *testing.T) {
	for in, want := range map[string]string{
		"curl \\\n  -u a:secretpw1 https://x":                     "curl \\\n  -u a:[REDACTED] https://x",
		`curl "https://x?a=1&b=2" -u a:secretpw1`:                 `curl "https://x?a=1&b=2" -u a:[REDACTED]`,
		`curl -H 'A: b;c|d' -u a:secretpw1 https://x`:             `curl -H 'A: b;c|d' -u a:[REDACTED] https://x`,
		`curl --user "admin:pass word" https://x`:                 `curl --user "admin:[REDACTED]" https://x`,
		`curl --user 'admin:pass word' https://x`:                 `curl --user 'admin:[REDACTED]' https://x`,
		`curl --proxy-user="a:pass word" https://x`:               `curl --proxy-user="a:[REDACTED]" https://x`,
		`curl -u a:"pa ss word" https://x`:                        `curl -u a:"[REDACTED]" https://x`,
		"CURL -u a:secretpw1 https://x":                           "CURL -u a:[REDACTED] https://x",
		"Wget --PASSWORD" + " x":                                  "Wget --PASSWORD x",
		"WGET --password=letmeinplease https://x":                 "WGET --password=[REDACTED] https://x",
		"cd /tmp && /usr/bin/curl -sS -u a:secretpw1 https://x":   "cd /tmp && /usr/bin/curl -sS -u a:[REDACTED] https://x",
		"curl https://x; curl -u a:secretpw1 https://y":           "curl https://x; curl -u a:[REDACTED] https://y",
		"echo hi | curl -K - -u a:secretpw1":                      "echo hi | curl -K - -u a:[REDACTED]",
		"curl -s -o /dev/null https://x -u a:secretpw1 -w '%{h}'": "curl -s -o /dev/null https://x -u a:[REDACTED] -w '%{h}'",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		"curl -u me:YOUR_API_KEY https://x",
		"curl -u me:API_TOKEN https://x",
		"curl -u me:PASSWORD_HERE https://x",
		"curl https://x\nsudo -u postgres:postgres1 psql",
		"curl https://x && sudo -u postgres:postgres1 psql",
		"curl -s https://x \\\n | sudo -u postgres:postgres1 psql",
	})
}

func TestCommandCredentialsInJSONEncodedText(t *testing.T) {
	for in, want := range map[string]string{
		`{"cmd":"curl -u admin:s3cretpw1"}`:                              `{"cmd":"curl -u admin:[REDACTED]"}`,
		`{"cmd":"curl -u a:s3cretpw1","next":"x"}`:                       `{"cmd":"curl -u a:[REDACTED]","next":"x"}`,
		`{"cmd":"curl -u admin:s3cretpw1\nnext word"}`:                   `{"cmd":"curl -u admin:[REDACTED]\nnext word"}`,
		`{"cmd":"curl -u admin:s3cretpw1\tnext"}`:                        `{"cmd":"curl -u admin:[REDACTED]\tnext"}`,
		`{"cmd":"curl -H \"A: b\" -u admin:s3cretpw1 https://x"}`:        `{"cmd":"curl -H \"A: b\" -u admin:[REDACTED] https://x"}`,
		`{"cmd":"curl --user \"admin:s3cretpw1\" https://x"}`:            `{"cmd":"curl --user \"admin:[REDACTED]\" https://x"}`,
		`{"cmd":"wget --password s3cretpw1\nls"}`:                        `{"cmd":"wget --password [REDACTED]\nls"}`,
		`{"cmd":"curl -u a:s3cretpw1\nsudo -u postgres:postgres1 psql"}`: `{"cmd":"curl -u a:[REDACTED]\nsudo -u postgres:postgres1 psql"}`,
		"curl -u admin:pa'ss1234 https://x":                              "curl -u admin:[REDACTED] https://x",
		"curl -u admin:pa'ss1234 -H 'X: y' https://x":                    "curl -u admin:[REDACTED] -H 'X: y' https://x",
		"curl -u admin:s3cretpw1 -d '{\"a\":1}' https://x":               "curl -u admin:[REDACTED] -d '{\"a\":1}' https://x",
		"curl -u \"admin:pass word\" https://x":                          "curl -u \"admin:[REDACTED]\" https://x",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		`{"cmd":"curl https://x\nsudo -u postgres:postgres1 psql"}`,
		`{"cmd":"docker run -u appuser:appgroup2 alpine"}`,
	})
}

func TestCommandCredentialsPlainShellQuotesAndEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`curl -u admin:Xk9vQ2mL"7pR4tY8wZ3nB6cF1" https://x`:      `curl -u admin:[REDACTED]" https://x`,
		`curl -u admin:Xk9vQ2mL"7pR4tY8wZ3nB6cF1 https://x`:       `curl -u admin:[REDACTED] https://x`,
		`curl -u admin:Xk9vQ2mL\t7pR4tY8wZ3nB6cF1 https://x`:      `curl -u admin:[REDACTED] https://x`,
		`curl -u admin:Xk9vQ2mL\n7pR4tY8wZ3nB6cF1 https://x`:      `curl -u admin:[REDACTED] https://x`,
		`curl -u admin:Xk9vQ2mL'7pR4tY8wZ3nB6cF1 https://x`:       `curl -u admin:[REDACTED] https://x`,
		`curl -u admin:pa"ss word" https://x`:                     `curl -u admin:[REDACTED] https://x`,
		`echo "x" && curl -u admin:Xk9v"Q2mL7pR4tY8wZ" https://x`: `echo "x" && curl -u admin:[REDACTED]" https://x`,
	} {
		got := RedactSecrets(in)
		if strings.Contains(got, "7pR4") || strings.Contains(got, "ss word") || strings.Contains(got, "Q2mL") {
			t.Errorf("RedactSecrets(%q) left password text: %q", in, got)
		}
		if got != want && !strings.Contains(in, "pa\"ss") {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		`curl https://x "a b" && sudo -u postgres:postgres1 psql`,
		`docker run -u appuser:appgroup2 "alpine:3"`,
	})
}

func TestJSONModeIgnoresQuotesInShellSingleQuotes(t *testing.T) {
	for _, in := range []string{
		`grep -c '"' data.csv && curl -u admin:Xk9vQ2mL\t7pR4tY8wZ3nB6cF1 https://x`,
		`echo '"' '"' '"' && curl -u admin:Xk9vQ2mL\n7pR4tY8wZ3nB6cF1 https://x`,
		`echo don't say "hi" && curl -u admin:Xk9vQ2mL\n7pR4tY8wZ3nB6cF1 https://x`,
	} {
		if got := RedactSecrets(in); strings.Contains(got, "7pR4") {
			t.Errorf("RedactSecrets(%q) left password text: %q", in, got)
		}
	}
	// A real JSON string context still enables the JSON cuts.
	in := `{"cmd":"grep -c '\"' f && curl -u a:s3cretpw1\nls"}`
	if got, want := RedactSecrets(in), `{"cmd":"grep -c '\"' f && curl -u a:[REDACTED]\nls"}`; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCookieNameSegmentsAndPlaceholders(t *testing.T) {
	for _, name := range []string{"remember_user_token", "_gh_sess", "connect.sid", "PHPSESSID", "laravel_session", "__Secure-3PSID", "JSESSIONID", "session_id", "csrftoken", "auth_token", "authToken", "XSRF-TOKEN", "sessionid"} {
		in := "Cookie: " + name + "=abcdefghijklmnop"
		if got := RedactSecrets(in); strings.Contains(got, "abcdefgh") {
			t.Errorf("session cookie %q not redacted: %q", name, got)
		}
	}
	// Names that do not read as session cookies are no longer a reason to keep
	// a token-shaped value: any non-preference cookie value is redacted.
	for in, want := range map[string]string{
		"Cookie: loginHint=abcdefghijklmnop": "Cookie: loginHint=[REDACTED]",
		"Cookie: tokenType=abcdefghijklmnop": "Cookie: tokenType=[REDACTED]",
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q) = %q, want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		"Cookie: session=YOUR_SESSION_ID",
		"Cookie: sid=REPLACE_ME_NOW",
		"Cookie: theme=abcdefghijklmnop",
	})
}

func TestCookieValueOnlyRedaction(t *testing.T) {
	for in, want := range map[string]string{
		"Cookie: sessionid=abcdefghijklmnopqrstuvwx":                     "Cookie: sessionid=[REDACTED]",
		"Cookie: JSESSIONID=ABCDEFGHIJKLMNOPQRSTUV; theme=dark":          "Cookie: JSESSIONID=[REDACTED]; theme=dark",
		"Set-Cookie: sid=abcdefgh12; Path=/; HttpOnly":                   "Set-Cookie: sid=[REDACTED]; Path=/; HttpOnly",
		"Cookie: lang=en-US; csrftoken=abcdefghijklmn; theme=dark":       "Cookie: lang=en-US; csrftoken=[REDACTED]; theme=dark",
		"Cookie: a=b; unknown=" + cfghunt13Secret + "; z=y":              "Cookie: a=b; unknown=[REDACTED]; z=y",
		`curl -H "Cookie: PHPSESSID=abcdefghijklmnop" https://x.example`: `curl -H "Cookie: PHPSESSID=[REDACTED]" https://x.example`,
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	cfghunt13ExpectUnchanged(t, []string{
		"Cookie: theme=dark; lang=en-US; consent=yes",
		"Cookie: preferences=compact-layout-wide",
		"Cookie: session=abc",
		"Cookie: session=$SESSION_VALUE_X",
		"Set-Cookie: session=; Max-Age=0",
	})
}

func TestWebhookPlaceholdersAndVariants(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		"https://hooks.slack" + ".com/triggers/T01ABCDEF/123456789012/" + cfghunt13Secret,
		"https://hooks.slack" + ".com/workflows/T01ABCDEF/A02GHIJKL/" + cfghunt13Secret,
	})
	cfghunt13ExpectUnchanged(t, []string{
		"https://hooks.slack" + ".com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX",
		"https://hooks.slack" + ".com/services/TXXXXXXXX/BXXXXXXXX/xxxxxxxxxxxxxxxxxxxxxxxx",
		"https://discord.com/api/webhooks/123456789012345678/XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
		"https://discord.com/api/webhooks/123456789012345678/your_webhook_token_goes_right_here_ok_1",
	})
}

func TestWebhookAndURLCorpusUnchanged(t *testing.T) {
	cfghunt13ExpectUnchanged(t, []string{
		"see https://hooks.slack" + ".com/services for the docs",
		"https://api.slack.com/messaging/webhooks",
		"https://discord.com/api/webhooks/123456789012345678",
		"https://discord.com/developers/docs/resources/webhook",
		"postgresql+psycopg2://db.internal/app",
		"rediss://cache.internal:6380/0",
		"mail me at a@b.com about redis://localhost:6379",
	})
}

func TestJSONEscapedSecretCorpus(t *testing.T) {
	cfghunt13ExpectRedacted(t, []string{
		`{"v":"secret: ` + cfghunt13Secret + `\r\nnext"}`,
		`{"v":"token=` + cfghunt13Secret + `\tx"}`,
		`{"token":"` + cfghunt13Secret + `\nfoo"}`,
		`{"content":"API_KEY=` + cfghunt13Secret + `\nDEBUG=1"}`,
	})
	// The text after the escape stays readable for the unquoted form.
	if got := RedactSecrets(`{"v":"token: ` + cfghunt13Secret + `\nlog_level: debug"}`); !strings.Contains(got, `\nlog_level: debug`) {
		t.Errorf("text after the escape was swallowed: %q", got)
	}
	cfghunt13ExpectUnchanged(t, []string{
		`{"v":"token: kubernetes-secret-name\nlog_level: debug"}`,
		`fmt.Printf("token: %s\n", tok)`,
		`{"v":"secret=C:\new\table\files\path\here\x"}`,
		`{"v":"token: short\nsecond line"}`,
		`{"v":"secret: ${SECRET_VALUE_FROM_ENV_VAR}\nnext"}`,
	})
}

// The new rules stay linear on hostile input.
func TestNewRedactionRulesScaleLinearly(t *testing.T) {
	gens := map[string]func(n int) string{
		"json escapes":       func(n int) string { return strings.Repeat(`token:\n\n\n\n`, n/12) },
		"escape tokens":      func(n int) string { return "token: " + strings.Repeat(`aB3\x`, n/5) },
		"cookie header":      func(n int) string { return strings.Repeat("Cookie: a=b;", n/12) },
		"cookie long":        func(n int) string { return "Cookie: " + strings.Repeat("a=Bc1;", n/6) },
		"curl flags":         func(n int) string { return strings.Repeat(" -u a:", n/6) },
		"curl repeated":      func(n int) string { return strings.Repeat("curl -x ", n/8) },
		"curl continuations": func(n int) string { return strings.Repeat("curl \\\n", n/7) },
		"curl quoted ops":    func(n int) string { return "curl " + strings.Repeat("'a;b|c&d' ", n/10) },
		"curl open quote":    func(n int) string { return strings.Repeat("curl '", n/6) },
		"curl open dquote":   func(n int) string { return "curl \"" + strings.Repeat("a\\ ", n/3) },
		"curl many cmds":     func(n int) string { return strings.Repeat("curl -u a;", n/10) },
		"curl json":          func(n int) string { return strings.Repeat(`{"c":"curl -u a:pw1234"}`, n/23) },
		"curl stray quotes":  func(n int) string { return strings.Repeat("curl -u a:p'q ", n/14) },
		"curl json escapes":  func(n int) string { return strings.Repeat(`curl -u a:\"b\n`, n/15) },
		"curl no end":        func(n int) string { return "curl " + strings.Repeat("-a ", n/3) },
		"cookie pairs":       func(n int) string { return "Cookie: " + strings.Repeat("sid=a;", n/6) },
		"cookie repeated":    func(n int) string { return strings.Repeat("Cookie:sid=", n/11) },
		"wget http":          func(n int) string { return strings.Repeat("wget http ", n/10) },
		"curl long user":     func(n int) string { return "curl -u " + strings.Repeat("a", n) },
		"db urls":            func(n int) string { return strings.Repeat("redis://:", n/9) },
		"db driver":          func(n int) string { return "postgresql+" + strings.Repeat("a", n) },
		"webhooks":           func(n int) string { return strings.Repeat("hooks.slack"+".com/services/TA/BA/", n/31) },
		"discord webhook":    func(n int) string { return strings.Repeat("discord.com/api/webhooks/1/", n/27) },
	}
	base := 64 << 10
	if raceEnabled {
		base = 16 << 10
	}
	for name, gen := range gens {
		small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 1)
		if small < 2*time.Millisecond {
			small = 2 * time.Millisecond
		}
		if large > 9*small {
			t.Errorf("%s: super-linear: %d bytes %v, x4 %v", name, base, small, large)
		}
		// 16x the input (64 KiB to 1 MiB) may not cost more than ~40x.
		if !raceEnabled && !testing.Short() {
			if huge := finalRedactTime(gen(16*base), 1); huge > 40*small {
				t.Errorf("%s: super-linear: %d bytes %v, x16 %v", name, base, small, huge)
			}
		}
	}
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
