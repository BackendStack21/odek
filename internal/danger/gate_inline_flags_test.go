package danger

import "testing"

// -e is code for ruby/perl/node but errexit for shells; -c is code for shells.
func TestInlinePayloadFlagPerInterpreter(t *testing.T) {
	for _, tc := range []struct {
		name, tok string
		want      bool
	}{
		{"bash", "-e", false},
		{"sh", "-e", false},
		{"zsh", "-eu", false},
		{"bash", "-c", true},
		{"bash", "-ec", true},
		{"ruby", "-e", true},
		{"perl", "-e", true},
		{"perl", "-le", true},
		{"python3", "-c", true},
		{"python3", "-m", false},
	} {
		if got := inlinePayloadFlag(tc.name, tc.tok); got != tc.want {
			t.Errorf("inlinePayloadFlag(%q, %q) = %v, want %v", tc.name, tc.tok, got, tc.want)
		}
	}
}
