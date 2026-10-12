package redact

import (
	"strings"
	"testing"
	"time"
)

func finalRedactTime(s string, runs int) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		RedactSecrets(s)
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	return best
}

// Rejected values must not make later names rescan the same text: quadrupling
// the input may not grow the time more than ~9x (quadratic: 16x).
func TestRED_RejectedCredentialValuesScaleLinearly(t *testing.T) {
	gens := map[string]func(n int) string{
		"rejected call":    func(n int) string { return strings.Repeat("api_key=a.b(", n/12) },
		"rejected var":     func(n int) string { return strings.Repeat("password=$X", n/11) },
		"rejected environ": func(n int) string { return strings.Repeat("token=os.environ[", n/17) },
		"repeated names":   func(n int) string { return strings.Repeat("TOKEN_X=.", n/9) },
		"long rejected":    func(n int) string { return "api_key=a.b(" + strings.Repeat("x", n) },
		"brackets":         func(n int) string { return strings.Repeat("{password=a]", n/12) },
		"rejected spaced":  func(n int) string { return strings.Repeat("secret_key=foo( ", n/16) },
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
		if !raceEnabled && !testing.Short() && finalRedactTime(gen(1<<20), 1) > 10*time.Second {
			t.Errorf("%s: 1 MiB too slow", name)
		}
	}
}

// A bracket inside an unquoted secret is part of the secret; it ends the
// value only when it closes a structure opened before the name.
func TestRED_BracketInsideSecretStaysRedacted(t *testing.T) {
	leaks := map[string]string{
		"DB_PASSWORD=Xk9}mQ2#vL8@pR4!zzQ7":                               "mQ2",
		"API_KEY=ab]cdEFGH1234ijklMNOP5678qrst":                          "cdEFGH",
		"client_secret=x}aB3xY9kL2mN8pQ4rS7tU1vW":                        "aB3xY9",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG}bPxRfiCYEXAMPLEKEY": "bPxRfi",
	}
	for in, tail := range leaks {
		if out := RedactSecrets(in); strings.Contains(out, tail) {
			t.Errorf("leak: %q -> %q", in, out)
		}
	}
	if out := RedactSecrets(`{password:Xk9mP2vL8qR4tZ12} ok`); !strings.HasSuffix(out, "} ok") || strings.Contains(out, "Xk9m") {
		t.Errorf("flow value: %q", out)
	}
	if out := RedactSecrets(`x: [api_key=Xk9mP2vL8qR4tZ12abcdef], y`); !strings.Contains(out, "], y") || strings.Contains(out, "Xk9m") {
		t.Errorf("flow list: %q", out)
	}
}

// Go and JS source and secret references stay readable.
func TestRED_SourceAndReferencesNotRedacted(t *testing.T) {
	for _, in := range []string{
		"bbp.Password = &password1234",
		"Password: &password1234,",
		"Password: *body.Password,",
		"Password: cfg.Password,",
		"Password: cfg.Password1)",
		`password = "@env:AC_PASSWORD"`,
		`SEARXNG_SECRET="$(openssl rand -hex 32)"`,
		"password = ~/.config/vault/pw1",
		`"github-mcp-token": {"github-mcp-token", false, 1},`,
		"Password: passwordFromVault,",
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("over-redacted: %q -> %q", in, out)
		}
	}
}

// camelCase and PascalCase names that end in a secret word carry secrets.
func TestRED_CamelCaseNamesWithSecretValue(t *testing.T) {
	for _, in := range []string{
		"GithubToken=aB3xY9kL2mN8pQ4rS7tU1vW",
		"export GithubToken=ghx_aB3xY9kL2mN8pQ4rS7tU1vW",
		"apiKey=aB3xY9kL2mN8pQ4rS7tU1vW",
		"clientSecret=aB3xY9kL2mN8pQ4rS7tU1vW",
	} {
		if out := RedactSecrets(in); strings.Contains(out, "aB3xY9") {
			t.Errorf("leak: %q -> %q", in, out)
		}
	}
	for _, in := range []string{"maxToken = computeTokenLimitFor(model)", "tokenCount=1234567890123456789012"} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("code redacted: %q -> %q", in, out)
		}
	}
}

// A skipped reference is consumed whole.
func TestRED_SkippedReferenceNotRedactedFromInside(t *testing.T) {
	for _, in := range []string{
		`access_token = "${ACCESS_TOKEN:-default_token_value_here}"`,
		`password: ${DB_PASSWORD:-fallback_password_value}`,
	} {
		if out := RedactSecrets(in); out != in {
			t.Errorf("partly redacted: %q -> %q", in, out)
		}
	}
}
