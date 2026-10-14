package redact

import (
	"strings"
	"testing"
	"time"
)

// redres14ExpectExact checks RedactSecrets output exactly.
func redres14ExpectExact(t *testing.T, cases map[string]string) {
	t.Helper()
	for in, want := range cases {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

// redres14ExpectHidden checks that no forbidden fragment survives redaction
// and that HasSecrets agrees.
func redres14ExpectHidden(t *testing.T, cases map[string][]string) {
	t.Helper()
	for in, frags := range cases {
		got := RedactSecrets(in)
		for _, f := range frags {
			if strings.Contains(got, f) {
				t.Errorf("RedactSecrets(%q) = %q still contains %q", in, got, f)
			}
		}
		if !HasSecrets(in) {
			t.Errorf("HasSecrets(%q) = false, want true", in)
		}
	}
}

func redres14ExpectUnchanged(t *testing.T, inputs []string) {
	t.Helper()
	for _, in := range inputs {
		if got := RedactSecrets(in); got != in {
			t.Errorf("over-redaction: RedactSecrets(%q) = %q", in, got)
		}
		if HasSecrets(in) {
			t.Errorf("over-detection: HasSecrets(%q) = true", in)
		}
	}
}

// A cookie in a Cookie / Set-Cookie header is server state sent back as a
// bearer value, whatever its name: a name that does not read as a session
// cookie must not let a short value through.
func TestRED_CookieUnknownNameShortValueLeaks(t *testing.T) {
	redres14ExpectHidden(t, map[string][]string{
		"Cookie: __Host-x=abcdefgh1234":                                      {"abcdefgh1234"},
		"Cookie: user=abcdefghijklmnop":                                      {"abcdefghijklmnop"},
		"Set-Cookie: user=abcdefghijklmnop; Path=/; HttpOnly":                {"abcdefghijklmnop"},
		"Set-Cookie: __Secure-pref=abcdefgh1234; Secure":                     {"abcdefgh1234"},
		`curl -H "Cookie: theme=dark; uid=abcdefgh1234" https://x.example`:   {"abcdefgh1234"},
		`{"headers":{"Cookie":"lang=en; user=abcdefghijklmnop"}}`:            {"abcdefghijklmnop"},
		"< set-cookie: remember=abcdefgh1234; Max-Age=3600":                  {"abcdefgh1234"},
		"Cookie: account=a1b2c3d4e5; theme=dark":                             {"a1b2c3d4e5"},
		"Cookie: loginHint=abcdefghijklmnop":                                 {"abcdefghijklmnop"},
		"Cookie: _ga=GA1.2.1234567890.1234567890; prefs=compact":             {"1234567890.1234567890"},
		"Set-Cookie: user=abcdefghijklmnop; Domain=sub1.example.com; Secure": {"abcdefghijklmnop"},
	})
	redres14ExpectExact(t, map[string]string{
		"Cookie: theme=dark; user=abcdefghijklmnop":                                        "Cookie: theme=dark; user=[REDACTED]",
		"Set-Cookie: user=abcdefghijklmnop; Path=/; Domain=sub1.example.com; SameSite=Lax": "Set-Cookie: user=[REDACTED]; Path=/; Domain=sub1.example.com; SameSite=Lax",
		"Set-Cookie: __Host-x=abcdefgh1234; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT": "Set-Cookie: __Host-x=[REDACTED]; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
	})
}

// Plain preference cookies, short values, placeholders and key=value text
// outside a cookie header stay readable.
func TestCookieRuleDoesNotOverRedact(t *testing.T) {
	redres14ExpectUnchanged(t, []string{
		"Cookie: theme=dark; lang=en-US; consent=yes",
		"Cookie: preferences=compact-layout-wide",
		"Cookie: theme=abcdefghijklmnop",
		"Cookie: user_locale=en-GB-oxendict",
		"Cookie: tokenType=Bearer",
		"Cookie: user=$USER_COOKIE_VALUE",
		"Cookie: user=YOUR_USER_ID",
		"Cookie: user=<your-user-cookie>",
		"Set-Cookie: user=; Max-Age=0",
		"Set-Cookie: Path=/; Domain=sub1.example.com; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly",
		"user=abcdefghijklmnop",
		"https://x.example/?user=abcdefghijklmnop&__Host-x=abcdefgh1234",
		"name: user=abcdefghijklmnop",
		"the cookie: a small piece of text a site stores in the browser",
		"cookie jar: /home/user/.cache/cookies.txt",
	})
}

// A password that contains a double quote is written as \" in JSON-encoded
// text (and in shell double-quoted text); the escaped quote is part of the
// value, so the whole value is redacted, not only the part before it.
func TestRED_EscapedQuoteInPasswordLeaksTail(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456\"GhIj12 next"}`:                     `{"out":"[REDACTED] next"}`,
		`{"out":"DB_PASS=Xy7kLm9pQ2zzAbCdEf3456\"GhIj12 next"}`:                    `{"out":"DB[REDACTED] next"}`,
		`{"out":"pass: Xy7kLm9pQ2zzAbCdEf3456\"GhIj12\nnext"}`:                     `{"out":"[REDACTED]\nnext"}`,
		`{"out":"pwd=Xy7kLm9pQ2zzAbCdEf3456\"GhIj12\"x next"}`:                     `{"out":"[REDACTED] next"}`,
		`{"out":"mytool --password Xy7kLm9pQ2zzAbCdEf3456\"GhIj12 next"}`:          `{"out":"mytool [REDACTED] next"}`,
		`{"out":"machine x login y password Xy7kLm9pQ2zzAbCdEf3456\"GhIj12 next"}`: `{"out":"machine x login y [REDACTED] next"}`,
		`echo "token: Xy7kLm9pQ2zzAbCdEf3456\"GhIj12 next"`:                        `echo "[REDACTED] next"`,
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456\"GhIj12"}`:                          `{"out":"[REDACTED]"}`,
		`{"cmd":"curl -u \"admin:Xy7kLm9\\\"pQ2zz\" https://x.example"}`:           `{"cmd":"curl -u \"admin:[REDACTED]\" https://x.example"}`,
		`{"cmd":"wget --password \"Xy7kLm9\\\"pQ2zz\" https://x.example"}`:         `{"cmd":"wget --password \"[REDACTED]\" https://x.example"}`,
		`{"out":"password=Xy7kLm9\"pQ2zz\nnext"}`:                                  `{"out":"[REDACTED]\nnext"}`,
		`{"out":"password=\"Xy7kLm9\\\"pQ2zz\" next"}`:                             `{"out":"[REDACTED] next"}`,
	})
}

// In JSON-encoded text a credential-named value ends at an encoded newline,
// return or tab and at the closing quote of the JSON string; what follows is
// the next line or the next member and stays readable.
func TestRED_JSONCredentialValueOverRedactsAfterEscape(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"out":"password=Xy7kLm9pQ2zz\nnext line"}`:                          `{"out":"[REDACTED]\nnext line"}`,
		`{"out":"db_password=Xy7kLm9pQ2zz\r\nnext"}`:                          `{"out":"[REDACTED]\r\nnext"}`,
		`{"out":"password=Xy7kLm9pQ2zz\tnext"}`:                               `{"out":"[REDACTED]\tnext"}`,
		`{"out":"password=Xy7kLm9pQ2zz"}`:                                     `{"out":"[REDACTED]"}`,
		`{"out":"password=Xy7kLm9pQ2zz","b":"c"}`:                             `{"out":"[REDACTED]","b":"c"}`,
		`{"out":"api_key=\"Xy7kLm9pQ2zzAAAAAAAAAAAA\"\nnext line"}`:           `{"out":"[REDACTED]\nnext line"}`,
		`{"out":"secret_key_base=Xy7kLm9pQ2zzAbCdEf3456\nnext line"}`:         `{"out":"[REDACTED]\nnext line"}`,
		`{"out":"GithubToken=Xy7kLm9pQ2zzAbCdEf3456\nnext line"}`:             `{"out":"[REDACTED]\nnext line"}`,
		`{"out":"PASSWORD=Xy7kLm9pQ2zz\\nlit more"}`:                          `{"out":"[REDACTED] more"}`,
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456"}`:                             `{"out":"[REDACTED]"}`,
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456","x":1}`:                       `{"out":"[REDACTED]","x":1}`,
		`{"out":"mytool --password Xy7kLm9pQ2zzAbCdEf3456"}`:                  `{"out":"mytool [REDACTED]"}`,
		`[{"a":"x"},{"out":"export API_KEY=Xy7kLm9pQ2zzAbCdEf3456\nls -la"}]`: `[{"a":"x"},{"out":"export [REDACTED]\nls -la"}]`,
	})
}

// Outside JSON strings an unquoted value still runs to the next blank, and a
// quote that does not end a JSON string is part of the value.
func TestCredentialValueQuoteHandlingOutsideJSON(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`export PASSWORD=Xy7kLm9"pQ2zz`:                `export [REDACTED]`,
		`say "hi there password=Abc"def123 rest`:       `say "hi there [REDACTED] rest`,
		`PASSWORD=Xy7kLm9pQ2zz\nliteral more`:          `[REDACTED] more`,
		`{"out":"password=Abc\"def\"123 rest"}`:        `{"out":"[REDACTED] rest"}`,
		`{"out":"password=Xy7kLm9pQ2zz\\\"tail rest"}`: `{"out":"[REDACTED] rest"}`,
	})
	redres14ExpectUnchanged(t, []string{
		`{"out":"password: required\nnext"}`,
		`{"out":"token: \"short\"","n":1}`,
		`{"msg":"set the password=\"${DB_PASSWORD}\"\nthen run"}`,
		`{"msg":"token: see docs\" and more"}`,
	})
}

// Escaped-quote continuation, JSON-aware value ends and the cookie rule stay
// linear on hostile input: a value continued through \" is never rescanned
// from a later match inside it.
func TestEscapedQuoteAndCookieRulesScaleLinearly(t *testing.T) {
	if raceEnabled {
		t.Skip("timing ratio")
	}
	gens := map[string]func(n int) string{
		"bare key chain":    func(n int) string { return strings.Repeat(`token:Xy7kLm9pQ2zzAbCdEf3456\"`, n/30) },
		"pwd chain":         func(n int) string { return strings.Repeat(`pwd=Xy7kLm9pQ2zzAbCdEf3456\"x`, n/29) },
		"flag chain":        func(n int) string { return "--password " + strings.Repeat(`Xy7kLm9pQ2zzAbCdEf3456\"`, n/24) },
		"json cred escapes": func(n int) string { return `{"o":"` + strings.Repeat(`password=\"Ab1\\\"`, n/18) },
		"json cred quotes":  func(n int) string { return `{"o":"` + strings.Repeat(`password=Ab1"x`, n/14) },
		"json open quotes":  func(n int) string { return `{"o":"` + strings.Repeat(`api_key=\"Ab1`, n/13) },
		"cookie unknown":    func(n int) string { return "Cookie: " + strings.Repeat("u=abcdefgh1;", n/12) },
		"curl dq escapes":   func(n int) string { return `{"c":"curl -u \"a:` + strings.Repeat(`\\\"`, n/4) },
	}
	base := 64 << 10
	for name, gen := range gens {
		small, large := finalRedactTime(gen(base), 2), finalRedactTime(gen(4*base), 1)
		if small < 2*time.Millisecond {
			small = 2 * time.Millisecond
		}
		if large > 12*small {
			t.Errorf("%s: super-linear: %d bytes %v, x4 %v", name, base, small, large)
		}
	}
}

// Edge cases of JSON-encoded value parsing: unclosed or truncated escaped
// quotes, other escapes inside a value, blanks before a structural character
// and over-long literals.
func TestJSONEncodedValueEdges(t *testing.T) {
	long := strings.Repeat("Ab1", 1500)
	redres14ExpectExact(t, map[string]string{
		`{"out":"api_key=\"Xy7kLm9pQ2zzAbCdEf3456\nnext"}`:    `{"out":"[REDACTED]\nnext"}`,
		`{"out":"api_key=\"Xy7k\/Lm9pQ2zzAbCdEf3456\" next"}`: `{"out":"[REDACTED] next"}`,
		`{"out":"password=\"Xy7kLm9pQ2zz`:                     `{"out":"[REDACTED]`,
		`{"out":"password=\"Xy7kLm9pQ2zz\`:                    `{"out":"[REDACTED]`,
		`{"out": "password=Xy7kLm9pQ2zz" , "b":1}`:            `{"out": "[REDACTED]" , "b":1}`,
		`{"out":"api_key=\"` + long + `\" next"}`:             `{"out":"[REDACTED] next"}`,
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456\"GhIj12\`:      `{"out":"[REDACTED]\`,
		`{"out":"token: Xy7kLm9pQ2zzAbCdEf3456\"Gh\/Ij12 x"}`: `{"out":"[REDACTED] x"}`,
	})
	redres14ExpectHidden(t, map[string][]string{
		"Cookie: __=abcdefgh1234": {"abcdefgh1234"},
	})
}
