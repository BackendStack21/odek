package config

import "testing"

func TestNormalizeThinking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"  ", "", true},
		{"disabled", "disabled", true},
		{"DISABLED", "disabled", true},
		{"low", "low", true},
		{" medium ", "medium", true},
		{"high", "high", true},
		{"mid", "medium", true},
		{"MID", "medium", true},
		{"enabled", "medium", true},
		{"on", "medium", true},
		{"true", "medium", true},
		{"1", "medium", true},
		{"max", "high", true},
		{"off", "disabled", true},
		{"false", "disabled", true},
		{"0", "disabled", true},
		{"banana", "", false},
		{"medium-high", "", false},
		{"enable", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeThinking(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeThinking(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCanonicalThinking(t *testing.T) {
	t.Parallel()
	if got := CanonicalThinking("enabled"); got != ThinkingMedium {
		t.Errorf("CanonicalThinking(enabled) = %q, want medium", got)
	}
	if got := CanonicalThinking(""); got != "" {
		t.Errorf("CanonicalThinking(\"\") = %q, want empty", got)
	}
	if got := CanonicalThinking("nope"); got != "" {
		t.Errorf("CanonicalThinking(nope) = %q, want empty", got)
	}
	if got := CanonicalThinking("disabled"); got != ThinkingDisabled {
		t.Errorf("CanonicalThinking(disabled) = %q, want disabled", got)
	}
}
