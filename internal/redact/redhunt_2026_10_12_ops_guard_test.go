package redact

import (
	"strings"
	"testing"
)

func TestOps12RedactOrdinaryOutputUntouched(t *testing.T) {
	for _, in := range []string{
		"--- PASS: TestSomethingLongEnoughToTrigger_ok (0.00s)",
		"--- PASS: TestSomething2LongEnoughToTrigger_ok (0.00s)",
		"    --- PASS: TestSub/case_number_1_long_enough_name (0.00s)",
		"token = strings.TrimSpace(header)",
		"token = strings.TrimSpace(headerValue1)",
		`secret = os.Getenv("X")`,
		"pass: github.com/long/import/path",
		"secret: kubernetes-secret-name-for-tls-cert",
		"secret: some.package.name.with.dots",
		"token: " + "http://example.com/a/long/url/path",
		"sortToken: short",
		"<token>short</token>",
		"<secret>kubernetes-secret-name-for-tls-cert</secret>",
		"password lookup_function_name_here",
		"--password-file /etc/some/long/path/to/file",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}

func TestOps12RedactExtraShapes(t *testing.T) {
	ops12AssertRedacted(t, []string{
		`<password type="encrypted">` + ops12Secret + `</password>`,
		`<password><![CDATA[` + ops12Secret + `]]></password>`,
		`githubToken: ` + ops12Secret,
		`jwtSecret = "` + ops12Secret + `"`,
		`app --secret-key ` + ops12Secret,
		`app --private-key ` + ops12Secret,
		`Authorization: Negotiate ` + ops12Secret,
	})
	if out := RedactSecrets(`<token>` + ops12Secret + `</token> rest`); out != "[REDACTED] rest" {
		t.Errorf("stray fragment: %q", out)
	}
	if strings.Contains(RedactSecrets("PASS_WORD=nothing"), "[REDACTED]") {
		t.Error("short value redacted")
	}
}
