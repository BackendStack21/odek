package danger

import "testing"

// A shell fed by a static producer through pass-through stages (cat, tee,
// sort, uniq, tac, head, tail) runs the producer's text unchanged, and a
// here-string on such a stage is the producer. The denylist must see it.
func TestRED_DenylistSeesStaticTextThroughPassThroughStages(t *testing.T) {
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		"echo 'git push' | tee f | sh",
		"echo 'git push' | cat | sh",
		"echo 'git push' | cat - | bash",
		"echo 'git push' | sort | uniq | bash",
		"printf 'git push\\n' | head -n 1 | sh",
		"printf 'git push\\n' | tail -n1 | tac | sh",
		"cat <<< 'git push' | sh",
		"cat <<< 'git push' | tee f | sh",
		"echo hi | cat <<< 'git push' | sh",
		"tee f <<< 'git push' | sh",
		"echo 'git push' | command cat | sudo sh",
	} {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s, want deny", cmd, got)
		}
	}
}

// Stages that change or replace the data are not pass-through: the text that
// reaches the shell is not the producer's, so nothing is denied on its account
// (the pipe into a shell still prompts as code execution).
func TestDenylistIgnoresNonPassThroughStages(t *testing.T) {
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		"echo 'git push' | grep x | sh",
		"echo 'git push' | cat file.txt | sh",
		"echo 'git push' | sort file.txt | sh",
		"cat <<< 'git push' | grep x | sh",
		"echo 'git status' | tee f | sh",
		"echo 'git push' | tee f | cat | grep x | sh",
	} {
		if got := cfg.ActionForCommand(cmd); got == Deny {
			t.Errorf("ActionForCommand(%q) = deny, want a prompt at most", cmd)
		}
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
}
