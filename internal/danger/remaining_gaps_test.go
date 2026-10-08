package danger

import "testing"

// Denylist entries match the command a line would run, including commands a
// wrapper's split string carries, commands behind another tool's global
// options, and commands built from variables whose value is known statically.
func TestRemaining_DenylistEnvSplitString(t *testing.T) {
	for _, cmd := range []string{
		`env -S 'git push'`,
		`env --split-string='git push'`,
		`env --split-string 'git push origin main'`,
		`env -S"git push"`,
		`env -i -S 'git push' `,
		`env FOO=1 -S 'git push'`,
	} {
		if !denylistMatch(cmd, []string{"git push"}) {
			t.Errorf("denylist `git push` must match %q", cmd)
		}
	}
	if denylistMatch(`env -S 'git status'`, []string{"git push"}) {
		t.Error("env -S 'git status' must not match `git push`")
	}
}

func TestRemaining_DenylistToolGlobalOptions(t *testing.T) {
	cases := []struct{ entry, cmd string }{
		{"docker push", `docker -H tcp://h:2375 push img`},
		{"docker push", `docker --host tcp://h:2375 push img`},
		{"docker push", `docker --context x push img`},
		{"docker push", `docker --config dir push img`},
		{"docker push", `docker -l debug push img`},
		{"docker push", `docker -D push img`},
		{"kubectl delete", `kubectl -n ns delete pod x`},
		{"kubectl delete", `kubectl --namespace ns delete pod x`},
		{"kubectl delete", `kubectl --context c delete pod x`},
		{"kubectl delete", `kubectl --kubeconfig /tmp/k delete pod x`},
		{"helm uninstall", `helm -n ns uninstall r`},
		{"helm uninstall", `helm --kube-context c uninstall r`},
		{"gh pr merge", `gh -R o/r pr merge 3`},
		{"gh pr merge", `gh --repo o/r pr merge 3`},
		{"npm run", `npm --prefix dir run x`},
		{"npm publish", `npm --registry http://r publish`},
		{"cargo run", `cargo +nightly run`},
		{"cargo publish", `cargo +stable publish`},
		{"terraform apply", `terraform -chdir=dir apply`},
		{"terraform destroy", `terraform -chdir dir destroy`},
	}
	for _, c := range cases {
		if !denylistMatch(c.cmd, []string{c.entry}) {
			t.Errorf("denylist %q must match %q", c.entry, c.cmd)
		}
	}
	for _, c := range []struct{ entry, cmd string }{
		{"docker push", `docker -H host ps`},
		{"docker push", `docker -H push ps`}, // `push` is the host value
		{"kubectl delete", `kubectl -n delete get pods`},
		{"gh pr merge", `gh -R pr pr view 3`},
		{"cargo run", `cargo +nightly build`},
		{"npm run", `npm --prefix run test`},
	} {
		if denylistMatch(c.cmd, []string{c.entry}) {
			t.Errorf("denylist %q must not match %q", c.entry, c.cmd)
		}
	}
}

func TestRemaining_DenylistStaticVariables(t *testing.T) {
	entries := []string{"git push"}
	for _, cmd := range []string{
		`g=git; $g push origin main`,
		`g=git; ${g} push origin main`,
		`cmd="git push"; $cmd`,
		`cmd='git push origin'; $cmd main`,
		`c=push; git $c`,
		`c=push; git $c origin main`,
		`g=git c=push; $g $c`,
		`c=push && git $c`,
		`export c=push; git $c`,
		`c=push; (git $c)`,
		`c=push; sh -c "git $c"`,
		`c=push; echo $(git $c)`,
		`cmd="git push"; "$cmd"`,
	} {
		if !denylistMatch(cmd, entries) {
			t.Errorf("denylist `git push` must match statically resolved %q", cmd)
		}
	}
	for _, cmd := range []string{
		`g=git; $g push-notes`,
		`c=push-notes; git $c`,
		`c=status; git $c`,
		`c=$(whoami); git $c`,
		`git $c`,
		`$cmd`,
		`c=push; c=$(whoami); git $c`,
		`c=push; read c; git $c`,
	} {
		if denylistMatch(cmd, entries) {
			t.Errorf("denylist `git push` must not match %q (value not statically push)", cmd)
		}
	}
}
