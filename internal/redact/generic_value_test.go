package redact

import (
	"strings"
	"testing"
)

func TestGenericCredential_ValueForms(t *testing.T) {
	cases := []struct {
		name, in, leak string
	}{
		{"double quoted specials", `password = "Sup3r$ecret Passw0rd!#1234"`, "Passw0rd"},
		{"single quoted specials", `api_key: 'Zx9qLm2RvT7wPk4n&Yb8sH@dJ3QrT'`, "dJ3QrT"},
		{"backtick quoted", "secret_key=`Zx9qLm2RvT7wPk4n$Yb8sHdJ3QrT`", "dJ3QrT"},
		{"unclosed quote", `password="Zx9qLm2RvT7wPk4nYb8s!HdJ3QrT`, "dJ3QrT"},
		{"unquoted specials", `passwd: Zx9qLm2RvT7wPk4nYb8s#HdJ3QrT`, "dJ3QrT"},
	}
	for _, c := range cases {
		if out := RedactSecrets(c.in); strings.Contains(out, c.leak) {
			t.Errorf("%s: leaked %q in %q", c.name, c.leak, out)
		}
	}
}

func TestGenericCredential_ProseKept(t *testing.T) {
	for _, in := range []string{
		"the password: is short",
		"reset your password = soon, then continue with the rest of this sentence",
		`password: "short"`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("prose changed: %q -> %q", in, out)
		}
	}
}

func TestGenericCredential_QuotedKeys(t *testing.T) {
	for _, in := range []string{
		`{"api_key": "Zx9qLm2RvT7wPk4nYb8sHd3J", "x": 1}`,
		`{'client_secret':'Zx9qLm2RvT7wPk4nYb8sHd3J'}`,
		`{"password":"Zx9qLm2R vT7wPk4nYb8s$Hd3J"}`,
	} {
		out := RedactSecrets(in)
		if strings.Contains(out, "Hd3J") {
			t.Errorf("quoted key leaked: %q -> %q", in, out)
		}
	}
}
