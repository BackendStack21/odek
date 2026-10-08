package danger

import "testing"

func denylistCfg(entries ...string) *DangerousConfig {
	// Every class is allowed so only the denylist can produce a Deny.
	allow := map[RiskClass]Action{}
	for _, cls := range []RiskClass{Safe, LocalWrite, SystemWrite, Persistence, NetworkEgress,
		CodeExecution, Install, UnreadExec, Unknown, Destructive} {
		allow[cls] = Allow
	}
	return &DangerousConfig{Classes: allow, Denylist: entries}
}

// A denylist entry applies to every command position, not only the start of
// the whole command line.
func TestDenylist_PerSegmentAndWrappers(t *testing.T) {
	cfg := denylistCfg("git push")
	denied := []string{
		"git push",
		"git push origin main",
		"true && git push",
		"false || git push",
		"echo x; git push",
		"echo x\ngit push",
		"echo x | git push",
		"(git push)",
		"( git push )",
		"{ git push; }",
		"! git push",
		"if true; then git push; fi",
		"git -C . push",
		"git -c user.name=x push origin",
		"git --no-pager push",
		"git --git-dir=.git push",
		"env git push",
		"env -i FOO=1 git push",
		"FOO=1 git push",
		"command git push",
		"nohup git push",
		"timeout 5 git push",
		"time git push",
		"sudo git push",
		"xargs git push",
		"/usr/bin/git push",
		"./git push",
		"\"git\" push",
		"g''it push",
		"bash -c 'git push'",
		"sh -c \"echo ok && git push\"",
		"bash -lc 'git push'",
		"echo $(git push)",
		"echo `git push`",
		"cat <(git push)",
		"eval 'git push'",
		"eval git push",
		`find . -exec git push \;`,
		"watch 'git push'",
		"bash -c 'bash -c \"git push\"'",
	}
	for _, cmd := range denied {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %v, want Deny", cmd, got)
		}
	}
}

// Matching is by whole leading tokens: a different subcommand that merely
// shares a string prefix is not denied.
func TestDenylist_TokenPrefixSemantics(t *testing.T) {
	cfg := denylistCfg("git push")
	for _, cmd := range []string{
		"git push-notes",
		"git pushall",
		"git status",
		"git log --oneline",
		"echo git push",
		"printf '%s' 'git push'",
		"grep 'git push' README.md",
		"echo git; echo push",
		"ls git push-dir",
	} {
		if got := cfg.ActionForCommand(cmd); got == Deny {
			t.Errorf("ActionForCommand(%q) = Deny, want not Deny", cmd)
		}
	}
}

// Multi-token and path-qualified entries follow the same rules.
func TestDenylist_EntryForms(t *testing.T) {
	cfg := denylistCfg("terraform apply", "/usr/bin/kubectl delete", "rm")
	for _, cmd := range []string{
		"cd infra && terraform apply -auto-approve",
		"terraform  apply",
		"kubectl delete pod x",
		"true; /opt/bin/kubectl delete ns y",
		"echo hi | xargs rm",
	} {
		if got := cfg.ActionForCommand(cmd); got != Deny {
			t.Errorf("ActionForCommand(%q) = %v, want Deny", cmd, got)
		}
	}
	for _, cmd := range []string{"terraform plan", "kubectl get pods", "rmdir x", "echo rm"} {
		if got := cfg.ActionForCommand(cmd); got == Deny {
			t.Errorf("ActionForCommand(%q) = Deny, want not Deny", cmd)
		}
	}
}
