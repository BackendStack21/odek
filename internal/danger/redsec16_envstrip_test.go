package danger

import "testing"

// Host-mode shell and background children strip secrets.env names unless
// the operator sets dangerous.strip_secrets_env_children to false.
func TestRED_StripSecretsEnvChildrenDefaultsOn(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DangerousConfig
		want bool
	}{
		{"nil receiver", (*DangerousConfig)(nil), true},
		{"unset field", &DangerousConfig{}, true},
		{"explicit true", &DangerousConfig{StripSecretsEnvChildren: boolPtr(true)}, true},
		{"explicit false opts out", &DangerousConfig{StripSecretsEnvChildren: boolPtr(false)}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.StripSecretsEnvChildrenEnabled(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
