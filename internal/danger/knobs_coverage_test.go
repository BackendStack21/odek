package danger

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestStripSecretsEnvChildrenEnabledNilSafe(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DangerousConfig
		want bool
	}{
		{"nil receiver", (*DangerousConfig)(nil), false},
		{"nil pointer field", &DangerousConfig{}, false},
		{"false", &DangerousConfig{StripSecretsEnvChildren: boolPtr(false)}, false},
		{"true", &DangerousConfig{StripSecretsEnvChildren: boolPtr(true)}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.StripSecretsEnvChildrenEnabled(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRESTApprovalFrictionEnabledNilSafe(t *testing.T) {
	cases := []struct {
		name string
		cfg  *DangerousConfig
		want bool
	}{
		{"nil receiver", (*DangerousConfig)(nil), false},
		{"nil pointer field", &DangerousConfig{}, false},
		{"false", &DangerousConfig{RESTApprovalFriction: boolPtr(false)}, false},
		{"true", &DangerousConfig{RESTApprovalFriction: boolPtr(true)}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.RESTApprovalFrictionEnabled(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
