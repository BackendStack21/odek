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
	}
	for _, c := range cases {
		out := RedactSecrets(c.input)
		// The long high-entropy body must not survive verbatim.
		if strings.Contains(out, strings.Repeat("A", 88)) || strings.Contains(out, strings.Repeat("aB", 20)) {
			t.Errorf("%s: secret body survived redaction: %.60s", c.name, out)
		}
	}
}
