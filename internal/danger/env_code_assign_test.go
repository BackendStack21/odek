package danger

import "testing"

func TestEnvCodeAssignmentForms(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, cmd := range []string{
		`PROMPT_COMMAND='rm -rf ~'; export PROMPT_COMMAND; bash -i`,
		`env -S "PROMPT_COMMAND='rm -rf ~' bash -i"`,
		`export PROMPT_COMMAND+='; rm -rf ~'`,
		`declare -x PROMPT_COMMAND='rm -rf ~'`,
		`PROMPT_COMMAND='rm -rf ~'`,
		`env PROMPT_COMMAND='rm -rf ~' bash -i`,
	} {
		if got := Classify(cmd); got != Destructive {
			t.Errorf("%q = %v, want destructive", cmd, got)
		}
	}
	// A bare append is not parsed as an assignment and fails closed.
	if got := Classify(`PROMPT_COMMAND+='; rm -rf ~'`); got == Safe {
		t.Errorf("bare append classified safe")
	}
	for _, cmd := range []string{`PS1='$ '`, `export PS1='\u@\h:\w$ '`, `PROMPT_COMMAND="echo hi"`} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("%q = %v, want safe", cmd, got)
		}
	}
}
