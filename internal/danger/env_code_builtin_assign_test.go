package danger

import "testing"

func TestEnvCodeBuiltinAssignmentForms(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, cmd := range []string{
		`printf -v PROMPT_COMMAND 'rm -rf ~'; export PROMPT_COMMAND; bash -i`,
		`printf -vPROMPT_COMMAND 'rm -rf ~'`,
		`printf -v PROMPT_COMMAND '%s' 'rm -rf ~'`,
		`read PROMPT_COMMAND <<<'rm -rf ~'; export PROMPT_COMMAND; bash -i`,
		`read -r PROMPT_COMMAND <<< 'rm -rf ~'`,
		`mapfile -t PROMPT_COMMAND <<<'rm -rf ~'`,
	} {
		if got := Classify(cmd); got != Destructive {
			t.Errorf("%q = %v, want destructive", cmd, got)
		}
	}
	// Unknown input source: fail closed.
	for _, cmd := range []string{`read PROMPT_COMMAND < /tmp/x`, `cat /tmp/x | read PROMPT_COMMAND`} {
		if got := Classify(cmd); got == Safe {
			t.Errorf("%q classified safe", cmd)
		}
	}
	for _, cmd := range []string{`printf -v PS1 '$ '`, `printf -v x '%s' hi`, `read name <<<'bob'`, `printf -v PROMPT_COMMAND 'echo hi'`} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("%q = %v, want safe", cmd, got)
		}
	}
}
