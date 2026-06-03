package danger

import "testing"

// TestBypass_KnownEvasions verifies finding S-H2 in IMPROVEMENTS_ROADMAP.md:
// the Classify() function fails to flag several common shell evasion tricks
// that ultimately execute the destructive command. Every case in this table
// should be classified as Destructive (or higher), but today most of them
// fall through to Safe.
//
// Each subtest is a separate finding so a fix for one case does not silence
// the others. Each name documents what the shell actually does.
//
// When the underlying classifier is hardened, these tests must continue to
// pass — they are the regression suite for the fix.
func TestBypass_KnownEvasions(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		// minWant is the lowest acceptable risk class. The classifier may
		// return anything at or above this rank — we only require that it
		// is not Safe / not below Destructive.
		minWant RiskClass
		why     string
	}{
		{
			name:    "command_substitution_dollar_paren",
			cmd:     "$(echo rm) -rf /",
			minWant: Destructive,
			why:     "shell expands $(echo rm) to rm and runs rm -rf /",
		},
		{
			name:    "command_substitution_backticks",
			cmd:     "`echo rm` -rf /",
			minWant: Destructive,
			why:     "backtick substitution runs rm -rf /",
		},
		{
			name:    "leading_backslash_alias_bypass",
			cmd:     `\rm -rf /`,
			minWant: Destructive,
			why:     "shell ignores alias but still runs rm",
		},
		{
			name:    "escaped_middle_char",
			cmd:     `r\m -rf /`,
			minWant: Destructive,
			why:     "shell collapses r\\m to rm",
		},
		{
			name:    "ifs_obfuscation",
			cmd:     "rm$IFS-rf$IFS/",
			minWant: Destructive,
			why:     "shell expands $IFS to whitespace, yielding rm -rf /",
		},
		{
			name:    "command_builtin",
			cmd:     "command rm -rf /",
			minWant: Destructive,
			why:     "command is a POSIX builtin that runs its argument verbatim",
		},
		{
			name:    "exec_wrapper",
			cmd:     "exec rm -rf /",
			minWant: Destructive,
			why:     "exec replaces the shell with rm -rf /",
		},
		{
			name:    "absolute_path_rm",
			cmd:     "/bin/rm -rf /",
			minWant: Destructive,
			why:     "absolute path /bin/rm is still rm; today classified only as system_write, missing the destructive flag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.cmd)
			if Rank(got) < Rank(tc.minWant) {
				t.Errorf("Classify(%q) = %v, want at least %v\nwhy: %s",
					tc.cmd, got, tc.minWant, tc.why)
			}
		})
	}
}

// TestBypass_KnownDetections is the control — these obvious destructive
// commands are already detected today. Their presence guards against an
// over-correction that breaks existing behaviour.
func TestBypass_KnownDetections(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"plain_rm_rf_root", "rm -rf /"},
		{"single_quoted_rm", "'rm' -rf /"},
		{"trailing_comment", "rm -rf / # comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.cmd)
			if Rank(got) < Rank(Destructive) {
				t.Errorf("Classify(%q) = %v, want >= destructive — regression in existing detection", tc.cmd, got)
			}
		})
	}
}
