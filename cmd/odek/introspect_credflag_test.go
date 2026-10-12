package main

import "testing"

// A credential flag's value is masked even when it starts with a dash; only a
// following long flag means the credential flag took no value.
func TestRedactCredentialArgs_DashPrefixedValue(t *testing.T) {
	for _, val := range []string{"-AbC123xyz", "-s3cr3t", "-"} {
		got := redactCredentialArgs([]string{"--token", val})
		if got[1] != redactedArgPlaceholder {
			t.Errorf("value %q leaked: %v", val, got)
		}
	}
	got := redactCredentialArgs([]string{"--password", "--verbose"})
	if got[1] != "--verbose" {
		t.Errorf("following flag masked: %v", got)
	}
}
