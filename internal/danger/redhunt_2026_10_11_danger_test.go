package danger

import "testing"

// redhuntAtLeast asserts every command classifies at least as severe as min
// and that the default policy does not auto-allow it.
func redhuntAtLeast(t *testing.T, min RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	cfg := &DangerousConfig{}
	for _, cmd := range cmds {
		got := Classify(cmd)
		if Rank(got) < Rank(min) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, min)
		}
		if act := cfg.ActionForCommand(cmd); act == Allow {
			t.Errorf("ActionForCommand(%q) = allow under default policy, want prompt or deny", cmd)
		}
	}
}

// redhuntExactly asserts each command classifies exactly as want (used to pin
// that benign neighbours of an escalated form are not over-classified).
func redhuntExactly(t *testing.T, want RiskClass, cmds ...string) {
	t.Helper()
	t.Setenv("HOME", "/home/user")
	for _, cmd := range cmds {
		if got := Classify(cmd); got != want {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
}

// git grep -O<pager> / --open-files-in-pager=<pager> runs <pager> through the
// shell with the matching file names: arbitrary code execution.
func TestRED_GitGrepOpenFilesInPagerRunsProgram(t *testing.T) {
	redhuntAtLeast(t, CodeExecution,
		`git grep -O'sh -c "rm -rf ~"' x`,
		`git grep --open-files-in-pager='sh -c id' x`,
		`git grep -n -Oid pattern`,
		`git grep -nO pattern`,
		`git grep --open-files pattern`,
		`git grep --no-index -Oid pattern`,
	)
	// A pattern value that happens to contain an O is not the pager option.
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-eOops", "x"}, false},
		{[]string{"-e", "-O", "x"}, false},
		{[]string{"--or", "x"}, false},
		{[]string{"-n", "--", "-O"}, false},
		{[]string{"-iO"}, true},
		{[]string{"--op=less"}, true},
	} {
		if got := gitGrepOpensPager(tc.args); got != tc.want {
			t.Errorf("gitGrepOpensPager(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
