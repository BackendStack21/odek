package redact

import (
	"strings"
	"testing"
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
