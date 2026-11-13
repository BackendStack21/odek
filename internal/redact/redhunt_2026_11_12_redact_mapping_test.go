package redact

import (
	"strings"
	"testing"
)

// In the MYSQL_PWD / SSHPASS mapping form an unquoted value is code only in
// a narrow set of shapes; a literal that merely holds a dot, a bang or an
// empty pair of parentheses is a password.
func TestRED_EnvPasswordMappingLiteralThatLooksLikeCode(t *testing.T) {
	for _, pw := range []string{
		"p4ss.w0rd", "S3cret.Pw9x", "!S3cretPw9x", "S3cretPw9x()", "S3cretPw9x[]",
		"my.pass", "Pw9x.S3cret.w0rd", "str", "config", "!!S3cretPw9x",
	} {
		for _, form := range []string{
			"MYSQL_PWD: %s",
			"      MYSQL_PWD: %s\n      MYSQL_USER: app",
			"SSHPASS: %s",
			`"MYSQL_PWD": %s`,
			`{"MYSQL_PWD": %s}`,
			"MYSQL_PWD = %s",
			"MYSQL_PWD: str = %s",
			"- name: MYSQL_PWD\n  value: %s",
		} {
			in := strings.Replace(form, "%s", pw, 1)
			out := RedactSecrets(in)
			if strings.Contains(out, pw) {
				t.Errorf("password readable\n in =%s\n out=%s", in, out)
			}
			if again := RedactSecrets(out); again != out {
				t.Errorf("not stable\n once =%s\n twice=%s", out, again)
			}
		}
	}
}

// Code, references, tags, comments and templates in the mapping form hold no
// password and stay as written.
func TestRED_EnvPasswordMappingCodeStaysReadable(t *testing.T) {
	for _, in := range []string{
		"MYSQL_PWD = cfg.Password",
		"MYSQL_PWD = os.getenv('DB_PASS')",
		`"MYSQL_PWD": process.env.DB_PASS,`,
		`"MYSQL_PWD": null`,
		`{"MYSQL_PWD": null}`,
		"MYSQL_PWD: ~",
		"MYSQL_PWD: None",
		"MYSQL_PWD: str = Field(default=None)",
		"MYSQL_PWD: Optional[str] = None",
		"MYSQL_PWD: str = settings.db.password",
		"MYSQL_PWD: !vault |",
		"MYSQL_PWD: !secret db_password",
		"MYSQL_PWD: !!str",
		"MYSQL_PWD: !!binary |",
		"MYSQL_PWD: # set in CI",
		"MYSQL_PWD: ${DB_PASSWORD}",
		`MYSQL_PWD: "{{ .Values.db.password }}"`,
		`MYSQL_PWD: '{{ .Values.db.password }}'`,
		`{"MYSQL_PWD": "{{ db_password }}"}`,
		`"env": "{\"MYSQL_PWD\": \"{{ db_password }}\"}"`,
		"MYSQL_PWD: {{ .Values.db.password }}",
		"MYSQL_PWD: settings.DB['PASSWORD']",
		`MYSQL_PWD = cfg["pw"]`,
		"MYSQL_PWD = self.password",
		"MYSQL_PWD = this.config.password;",
		"MYSQL_PWD = var.db_password",
		"MYSQL_PWD = local.db_password",
		"MYSQL_PWD = module.db.password",
		"MYSQL_PWD: secrets.DB_PASSWORD",
		"MYSQL_PWD = System.getenv(\"DB_PASS\")",
		"MYSQL_PWD = ENV['DB_PASS']",
		"MYSQL_PWD = ENV.fetch('DB_PASS')",
		"MYSQL_PWD = os.environ.copy()",
		"MYSQL_PWD = decrypt(blob)",
		"MYSQL_PWD = lookup('env', 'DB_PASS')",
		"SSHPASS: params.ssh_password",
		// A literal under a known root reads as code.
		"MYSQL_PWD: data.w0rd",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("changed\n in =%s\n out=%s", in, out)
		}
	}
}

// A quoted value that is more than one template is a literal.
func TestRED_EnvPasswordMappingQuotedLiteralRedacted(t *testing.T) {
	for in, want := range map[string]string{
		`MYSQL_PWD: "S3cretPw9x"`:               `MYSQL_PWD: "[REDACTED]"`,
		`MYSQL_PWD: "{{ prefix }}S3cretPw9x"`:   `MYSQL_PWD: "[REDACTED]"`,
		`MYSQL_PWD: "S3cretPw9x{{ suffix }}"`:   `MYSQL_PWD: "[REDACTED]"`,
		`MYSQL_PWD: "{{ S3cretPw9x"`:            `MYSQL_PWD: "[REDACTED]"`,
		"MYSQL_PWD = getpass()":                 "MYSQL_PWD = [REDACTED])",
		`MYSQL_PWD: "{{ a }}" 'S3cretPw9x'`:     `MYSQL_PWD: "{{ a }}" 'S3cretPw9x'`,
		`MYSQL_PWD: "{{ a }}' "S3cretPw9x"`:     `MYSQL_PWD: "[REDACTED]"[REDACTED]"`,
		`setenv("MYSQL_PWD", "S3cret.Pw9x", 1)`: `setenv("MYSQL_PWD", "[REDACTED]", 1)`,
	} {
		if out := RedactSecrets(in); out != want {
			t.Errorf("in  =%s\n out =%s\n want=%s", in, out, want)
		}
	}
}
