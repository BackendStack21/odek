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

// A bare token / secret / pwd / pass value in escaped quotes (JSON-encoded
// "token: \"...\"") is a quoted literal and is judged without its quotes.
func TestRED_EscapedQuotedBareKeyValueLeaks(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"out":"token: \"Xy7kLm9pQ2zzAbCdEf3456\" next"}`:  `{"out":"token: \"[REDACTED]\" next"}`,
		`{"out":"secret=\"Xy7kLm9pQ2zzAbCdEf3456\"\nnext"}`: `{"out":"secret=\"[REDACTED]\"\nnext"}`,
		`{"out":"MYSQL_PWD=\"Xy7kLm9pQ2zzAbCdEf3456\";ls"}`: `{"out":"MYSQL_PWD=\"[REDACTED]\";ls"}`,
	})
	redres14ExpectUnchanged(t, []string{
		`{"out":"secret: \"kubernetes-secret-name-for-app\""}`,
		`{"out":"token: \"${GITHUB_TOKEN_VALUE_HERE}\"\nnext"}`,
		`{"out":"pass: \"TestEscapedQuotedBareKeyValue\""}`,
		`{"out":"pwd=\"/home/user/projects/app-server\""}`,
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
		"cookie quoted":     func(n int) string { return "Cookie: " + strings.Repeat(`a="abcdefgh1"; `, n/16) },
		"cookie open quote": func(n int) string { return strings.Repeat(`Cookie: a="`, n/11) },
		"cookie nested":     func(n int) string { return "Cookie: a=" + strings.Repeat("cookie:", n/7) },
		"cookie escapes":    func(n int) string { return strings.Repeat(`Cookie: a=abcdefgh1\n`, n/20) },
		"cookie commas":     func(n int) string { return "Set-Cookie: " + strings.Repeat(", "+strings.Repeat("x", 300), n/302) },
		"cookie json":       func(n int) string { return `{"h":"Cookie: ` + strings.Repeat(`a=\"abcdefgh1\"; `, n/18) },
		"curl cookies":      func(n int) string { return strings.Repeat("curl -b a=abcdefgh1;b=c ", n/24) },
		"escaped keys":      func(n int) string { return `{"o":"{` + strings.Repeat(`\"password\":\"Xy7kLm9pQ2zz\",`, n/31) },
		"escaped key nulls": func(n int) string { return `{"o":"{` + strings.Repeat(`\"password\":null,`, n/17) },
		"cookie arrays":     func(n int) string { return `{"set-cookie":[` + strings.Repeat(`"a=abcdefgh1", `, n/15) },
		"setheader calls":   func(n int) string { return strings.Repeat(`set('cookie', 'a=abcdefgh1');`, n/29) },
		"setheader arrays":  func(n int) string { return `setHeader('Set-Cookie', [` + strings.Repeat(`'a=b', `, n/7) },
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

// A quoted cookie value is one unit: it is judged without its quotes, and a
// quoted value earlier in the header does not end the header.
func TestRED_QuotedCookieValueLeaks(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`Cookie: sessionid="abcdef123456789"`:                        `Cookie: sessionid="[REDACTED]"`,
		`Set-Cookie: sessionid="abcdef123456789"; Path=/`:            `Set-Cookie: sessionid="[REDACTED]"; Path=/`,
		`Cookie: a="xyz"; sessionid=abcdef123456789`:                 `Cookie: a="xyz"; sessionid=[REDACTED]`,
		`cookie: foo="abcdefghijkl"`:                                 `cookie: foo="[REDACTED]"`,
		`Cookie: foo='abcdefghijkl'; theme=dark`:                     `Cookie: foo='[REDACTED]'; theme=dark`,
		`curl -H 'Cookie: sid=abcdef123456789' https://x.example`:    `curl -H 'Cookie: sid=[REDACTED]' https://x.example`,
		`curl -H "Cookie: theme=dark; sid=abcdef123456789" -v x`:     `curl -H "Cookie: theme=dark; sid=[REDACTED]" -v x`,
		`{"h":"Cookie: a=\"xyz\"; sid=\"abcdef123456789\"; x=1"}`:    `{"h":"Cookie: a=\"xyz\"; sid=\"[REDACTED]\"; x=1"}`,
		`{"headers":{"Cookie":"theme=dark; user=abcdefghijklmnop"}}`: `{"headers":{"Cookie":"theme=dark; user=[REDACTED]"}}`,
	})
	redres14ExpectUnchanged(t, []string{
		`Cookie: theme="dark"; lang="en-US"`,
		`curl -H "Cookie: theme=dark" https://x.example/abcdefghijklmnop`,
		`Cookie: sid="unterminated abcdefghijkl`,
		`Cookie: a="xyz" sid`,
	})
}

// A cookie header inside JSON-encoded or escaped text ends at a literal \r,
// \n or \t escape and at the quote that closes the JSON string.
func TestRED_EscapedCookieHeaderLeaks(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`Cookie: sid=abcdef123456789\r\nHost: x`:                      `Cookie: sid=[REDACTED]\r\nHost: x`,
		`{"h":"Cookie: sid=abcdef123456789\r\nHost: x"}`:              `{"h":"Cookie: sid=[REDACTED]\r\nHost: x"}`,
		`Cookie: foo=abcdefghijkl\nCookie: bar=abcdefghijkl`:          `Cookie: foo=[REDACTED]\nCookie: bar=[REDACTED]`,
		`{"h":"Cookie: sid=abcdef123456789"}`:                         `{"h":"Cookie: sid=[REDACTED]"}`,
		`{"h":"Set-Cookie: sid=abcdef123456789\tPath=/abcdefghijkl"}`: `{"h":"Set-Cookie: sid=[REDACTED]\tPath=/abcdefghijkl"}`,
	})
	redres14ExpectUnchanged(t, []string{
		`{"h":"Cookie: theme=dark\r\nHost: abcdefghijklmnop.example"}`,
		`{"h":"Cookie: theme=dark","x":"user=abcdefghijklmnop"}`,
	})
}

// Folded Set-Cookie headers separate cookies with ", "; curl passes cookies
// with -b / --cookie. Both go through the same cookie-pair rule.
func TestRED_SetCookieCommaAndCurlCookieLeaks(t *testing.T) {
	redres14ExpectHidden(t, map[string][]string{
		"Set-Cookie: foo=abcdefghijkl, bar=zzzzzzzzzzzz": {"abcdefghijkl"},
	})
	redres14ExpectExact(t, map[string]string{
		"Set-Cookie: foo=abcdefghijkl, bar=Zq8xLm2pWr5t; Path=/":                        "Set-Cookie: foo=[REDACTED], bar=[REDACTED]; Path=/",
		"Set-Cookie: sid=abcdefgh12; Expires=Wed, 21 Oct 2026 07:28:00 GMT; theme=dark": "Set-Cookie: sid=[REDACTED]; Expires=Wed, 21 Oct 2026 07:28:00 GMT; theme=dark",
		"curl --cookie foo=abcdefghijkl https://x.example":                              "curl --cookie foo=[REDACTED] https://x.example",
		"curl --cookie=foo=abcdefghijkl https://x.example":                              "curl --cookie=foo=[REDACTED] https://x.example",
		"curl -b 'foo=abcdefghijkl; theme=dark' https://x.example":                      "curl -b 'foo=[REDACTED]; theme=dark' https://x.example",
		`curl -sb "sid=abcdef123456789" https://x.example`:                              `curl -sb "sid=[REDACTED]" https://x.example`,
		`{"cmd":"curl -b \"foo=abcdefghijkl\" https://x.example"}`:                      `{"cmd":"curl -b \"foo=[REDACTED]\" https://x.example"}`,
	})
	redres14ExpectUnchanged(t, []string{
		"curl -b cookies.txt https://x.example",
		"curl -b /home/user/.cache/cookies-abcdefghijkl.txt https://x.example",
		"curl -c jar.txt -b jar.txt https://x.example",
		"curl -b 'theme=dark; lang=en-US' https://x.example",
		"mytool -b foo=abcdefghijkl",
		"Set-Cookie: theme=dark, lang=en-US",
	})
}

// Set-Cookie attributes stay readable in their common spellings.
func TestRED_SetCookieAttributeVariants(t *testing.T) {
	redres14ExpectUnchanged(t, []string{
		"Set-Cookie: theme=dark; max_age=31536000; Path=/",
		"Set-Cookie: theme=dark; MAX-AGE=31536000; SAMESITE=Strict",
		"Set-Cookie: theme=dark; Max_Age=31536000; Same_Site=Lax; Http_Only",
	})
	redres14ExpectExact(t, map[string]string{
		"Set-Cookie: sid=abcdefgh12; max_age=31536000": "Set-Cookie: sid=[REDACTED]; max_age=31536000",
	})
}

// A credential under a quoted key (JSON, JS, Python dicts) keeps the key and
// its quotes: only the value inside its quotes is replaced, so the document
// stays well-formed.
func TestRED_QuotedKeyCredentialBreaksStructure(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"password":"abcdefghijklmnopqrstuvwx\"yzABCDEFGH","x":1}`: `{"password":"[REDACTED]","x":1}`,
		`{"password":"Xy7kLm9pQ2zz","x":1}`:                         `{"password":"[REDACTED]","x":1}`,
		`{"api_key": "Xy7kLm9pQ2zzAbCdEf3456"}`:                     `{"api_key": "[REDACTED]"}`,
		`{'db_password': 'Xy7kLm9pQ2zz'}`:                           `{'db_password': '[REDACTED]'}`,
	})
	redres14ExpectExact(t, map[string]string{
		`password: "Xy7kLm9pQ2zz"`:            `[REDACTED]`,
		`{"password":Xy7kLm9pQ2zz,"x":1}`:     `{"[REDACTED],"x":1}`,
		`{"out":"password=Xy7kLm9pQ2zz"}`:     `{"out":"[REDACTED]"}`,
		`{"password":"${DB_PASSWORD}","x":1}`: `{"password":"${DB_PASSWORD}","x":1}`,
	})
}

// Edge cases of the cookie pair scanner and of the bare-key value shape.
func TestCookieScannerAndShapeEdges(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"h":"Cookie: sid=\"abc\\\"def12345\"; x=1"}`:  `{"h":"Cookie: sid=\"[REDACTED]\"; x=1"}`,
		`{"h":"Cookie: sid=abc\/def12345; x=1"}`:        `{"h":"Cookie: sid=[REDACTED]; x=1"}`,
		`{"h":"Cookie: theme=\"dark\\nmore\"; x=1"}`:    `{"h":"Cookie: theme=\"dark\\nmore\"; x=1"}`,
		`Cookie: a="abc\"defghijk"; theme=dark`:         `Cookie: a="[REDACTED]"; theme=dark`,
		`curl -H 'Cookie: sid='abcdefghijkl x`:          `curl -H 'Cookie: sid='abcdefghijkl x`,
		`'a' {password=Xy7kLm9pQ2zz"}`:                  `'a' {[REDACTED]"}`,
		`{"out":"token: %Xy7kLm9pQ2zzAbCdEf3456"}`:      `{"out":"token: %Xy7kLm9pQ2zzAbCdEf3456"}`,
		`{"out":"token: xy7!klm9pq2zzabcdef3456 more"}`: `{"out":"[REDACTED] more"}`,
		`{"h":"Cookie: sid=\"abc\ndef12345\"; x=1"}`:    `{"h":"Cookie: sid=\"abc\ndef12345\"; x=1"}`,
		`{"h":"Cookie: sid=\"abcdef12345\"}`:            `{"h":"Cookie: sid=\"[REDACTED]\"}`,
		`{"h":"Cookie: sid=\"abcdef12345"}`:             `{"h":"Cookie: sid=\"[REDACTED]"}`,
		`Cookie: sid="abcdef12345`:                      `Cookie: sid="[REDACTED]`,
		`Cookie: sid='abcdef12345\nHost: x`:             `Cookie: sid='[REDACTED]\nHost: x`,
	})
	redres14ExpectHidden(t, map[string][]string{
		"Cookie: XMLHttpSession=abcdefgh12": {"abcdefgh12"},
	})
}

// JSON encoded inside a JSON string escapes its key quotes (\"password\":);
// the credential under such a key is redacted and the structure kept.
func TestRED_EscapedKeyJSONInJSONLeaks(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"out":"{\"password\":\"Xy7kLm9pQ2zz\",\"x\":1}"}`:           `{"out":"{\"password\":\"[REDACTED]\",\"x\":1}"}`,
		`{"out":"{\"api_key\": \"Xy7kLm9pQ2zzAbCdEf3456\"}"}`:         `{"out":"{\"api_key\": \"[REDACTED]\"}"}`,
		`{"out":"{\"token\":\"Xy7kLm9pQ2zzAbCdEf3456\",\"n\":2}"}`:    `{"out":"{\"token\":\"[REDACTED]\",\"n\":2}"}`,
		`{"out":"{\"db_password\":\"Xy7kLm9\\\"pQ2zz\"}"}`:            `{"out":"{\"db_password\":\"[REDACTED]\"}"}`,
		`{"out":"{\"Set-Cookie\":\"sid=abcdefgh12345678; Path=/\"}"}`: `{"out":"{\"Set-Cookie\":\"sid=[REDACTED]; Path=/\"}"}`,
	})
	redres14ExpectUnchanged(t, []string{
		`{"out":"{\"password\":\"${DB_PASSWORD}\",\"x\":1}"}`,
		`{"out":"{\"password\":null,\"token\":\"short\"}"}`,
		`{"out":"{\"token_count\":\"12345678901234567890123\"}"}`,
	})
}

// Set-Cookie values in a JSON array and in a setHeader / set / append call
// are cookie header values.
func TestRED_SetCookieArrayAndCallLeaks(t *testing.T) {
	redres14ExpectExact(t, map[string]string{
		`{"set-cookie":["sid=abcdefgh12345678; Path=/"]}`:                                           `{"set-cookie":["sid=[REDACTED]; Path=/"]}`,
		`{"Set-Cookie": [ "sid=abcdefgh12345678; Path=/", "theme=dark", "u=abcdefghijkl" ], "x":1}`: `{"Set-Cookie": [ "sid=[REDACTED]; Path=/", "theme=dark", "u=[REDACTED]" ], "x":1}`,
		`res.setHeader('Set-Cookie', 'sid=abcdefgh12345678; HttpOnly')`:                             `res.setHeader('Set-Cookie', 'sid=[REDACTED]; HttpOnly')`,
		`w.Header().Set("Set-Cookie", "sid=abcdefgh12345678; Path=/")`:                              `w.Header().Set("Set-Cookie", "sid=[REDACTED]; Path=/")`,
		`headers.append("cookie", "user=abcdefghijklmnop")`:                                         `headers.append("cookie", "user=[REDACTED]")`,
		`res.setHeader('Set-Cookie', ['a=abcdefgh12345678', 'b=Zq8xLm2pWr5t'])`:                     `res.setHeader('Set-Cookie', ['a=[REDACTED]', 'b=[REDACTED]'])`,
	})
	redres14ExpectUnchanged(t, []string{
		`{"set-cookie":["theme=dark; Path=/"]}`,
		`res.setHeader('Set-Cookie', cookieValue)`,
		`["cookie", "user=abcdefghijklmnop"]`,
		`log("cookie", "user=abcdefghijklmnop")`,
		`{"cookies":["user=abcdefghijklmnop"]}`,
	})
}
