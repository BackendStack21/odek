package danger

import "testing"

func TestDenylistStaticShellInputVariants(t *testing.T) {
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	for _, cmd := range []string{
		"echo 'echo hi; git push' | sh",
		"printf 'git push' | sudo bash",
		"echo git push | env bash",
		"echo 'git push' | tee /dev/null | sh && true",
		"zsh <<< 'ls; git push origin'",
		"echo 'git push' | sh -s",
	} {
		want := Deny
		if cmd == "echo 'git push' | tee /dev/null | sh && true" {
			// The producer is not directly upstream of the shell, so the text
			// is not statically known; the pipe stays a prompt, never allow.
			if got := cfg.ActionForCommand(cmd); got == Allow {
				t.Errorf("ActionForCommand(%q) = allow", cmd)
			}
			continue
		}
		if got := cfg.ActionForCommand(cmd); got != want {
			t.Errorf("ActionForCommand(%q) = %s, want %s", cmd, got, want)
		}
	}
	for _, cmd := range []string{
		"echo 'git status' | sh",
		"echo 'git push' | cat",
		"echo 'git push' | sh -c 'echo hi'",
		"sh <<< 'git status'",
		"echo \"$X\" | sh",
	} {
		if got := cfg.ActionForCommand(cmd); got == Deny {
			t.Errorf("ActionForCommand(%q) = deny, want no denylist match", cmd)
		}
	}
}
