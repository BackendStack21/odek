package redact

import (
	"strings"
	"testing"
)

// Security review wave C, F1: provider credential formats the pattern set
// missed — Azure storage AccountKey (connection strings), Stripe webhook
// signing + live API keys. Assemble at runtime where possible; these
// formats are structural (not real secrets).
func TestRedactSecrets_ProviderPatternGaps(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			"azure account key",
			"DefaultEndpointsProtocol=https;AccountName=st;AccountKey=" + strings.Repeat("A", 88) + "==;EndpointSuffix=core.windows.net",
		},
		{
			"stripe webhook secret",
			"whsec_" + strings.Repeat("aB", 21),
		},
		{
			"stripe live secret key",
			"sk_live_" + strings.Repeat("aB", 24),
		},
		{
			"stripe live restricted key",
			"rk_live_" + strings.Repeat("aB", 24),
		},
		{
			"github oauth token",
			"gho_" + strings.Repeat("A", 36),
		},
		{
			"npm access token",
			"npm_" + strings.Repeat("B", 36),
		},
		{
			"gitlab personal token",
			"glpat-" + strings.Repeat("C", 20),
		},
		{
			"pypi api token",
			"pypi-" + strings.Repeat("D", 20),
		},
	}
	for _, c := range cases {
		out := RedactSecrets(c.input)
		if out == c.input {
			t.Errorf("%s: input survived redaction unchanged: %.60s", c.name, out)
			continue
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: missing [REDACTED] marker: %.60s", c.name, out)
		}
	}
}
