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

func classifyIs(t *testing.T, cmd string, want RiskClass) {
	t.Helper()
	if got := Classify(cmd); got != want {
		t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
	}
}

// Reading a credential into the model's context is an environment-dump
// equivalent: any reference to a secret-bearing variable needs approval, even
// through a display verb.
func TestSecretEnvReferences_SystemWrite(t *testing.T) {
	for _, cmd := range []string{
		"echo $GITHUB_TOKEN",
		`echo "$GITHUB_TOKEN"`,
		"echo ${GITHUB_TOKEN}",
		`echo "${OPENAI_API_KEY}"`,
		"echo ${GITHUB_TOKEN:-x}",
		"echo ${#GITHUB_TOKEN}",
		"printf '%s' $AWS_SECRET_ACCESS_KEY",
		"printenv AWS_SECRET_ACCESS_KEY",
		"printenv GH_TOKEN",
		"env | grep GITHUB_TOKEN",
		"echo $MY_SERVICE_PASSWORD",
		"echo $DB_PASSWD",
		"echo $STRIPE_SECRET_KEY",
		"echo $TLS_PRIVATE_KEY",
		"echo $AZURE_CLIENT_SECRET",
		"echo $GOOGLE_APPLICATION_CREDENTIALS",
		"echo $DATABASE_URL",
		"echo $ANTHROPIC_API_KEY",
		"echo $SLACK_BOT_TOKEN",
		"echo $TELEGRAM_BOT_TOKEN",
		"echo $ODEK_API_KEY",
		"echo $ODEK_SOMETHING_KEY_FILE",
		"T=$GITHUB_TOKEN",
		"export X=$NPM_TOKEN",
		"cat file | sed s/x/$GITHUB_TOKEN/",
		"curl -H \"Authorization: Bearer $GITHUB_TOKEN\" https://api.github.com/user",
		"echo $(printenv NPM_TOKEN)",
		"bash -c 'echo $GITHUB_TOKEN'",
		"declare -p GITHUB_TOKEN",
		"cat <<EOF\ntoken=$GITHUB_TOKEN\nEOF",
		`jq -n 'env.GITHUB_TOKEN'`,
		`awk 'BEGIN{print ENVIRON["GITHUB_TOKEN"]}'`,
		`python3 -c "import os;print(os.environ['OPENAI_API_KEY'])"`,
	} {
		got := Classify(cmd)
		if Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
}

// Credential files in the workspace (not just home-anchored ones) need
// approval to read or write.
func TestCredentialFiles_SystemWrite(t *testing.T) {
	for _, cmd := range []string{
		"cat .env",
		"cat ./.env",
		"cat .env.local",
		"cat .env.production",
		"cat config/credentials.json",
		"cat service-account.json",
		"cat service-account-prod.json",
		"cat terraform.tfstate",
		"cat terraform.tfstate.backup",
		"cat prod.tfvars",
		"cat .git-credentials",
		"cat .npmrc",
		"cat .pypirc",
		"cat kubeconfig",
		"cat dev.kubeconfig",
		"cat *.pem",
		"cat server.pem",
		"cat tls/server.key",
		"cat id_rsa",
		"cat keys/id_ed25519",
		"cat sub/dir/id_ecdsa",
		"cat id_dsa",
		"cat secrets.yaml",
		"cat secrets.json",
		"cat .netrc",
		"cat release.keystore",
		"cat app.jks",
		"cat cert.p12",
		"cat cert.pfx",
		"head -n 3 .env",
		"grep foo .env",
		"grep -r TOKEN .env",
		"sed -n 1p .env",
		"base64 .env",
		"wc -l < .env",
		"cat < .env",
		"tail -f /srv/app/.env",
		"dd if=id_rsa of=/dev/null",
		"cat --show-all .env",
		"docker run --env-file=.env img",
		"echo x > .env",
		"echo x >> .npmrc",
		"cp .env.example .env",
		"cp /tmp/x config/credentials.json",
		"tee .env < /dev/null",
		"mv .env .env.bak",
		"cat 'my secrets/.env'",
	} {
		got := Classify(cmd)
		if Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
}

// Ordinary development commands stay quiet.
func TestSecretReadHeuristics_StaySafe(t *testing.T) {
	for _, cmd := range []string{
		"ls",
		"ls -la",
		"git log --oneline",
		"grep -r TOKEN src/",
		"grep -rn API_KEY .",
		"grep GITHUB_TOKEN README.md",
		"grep id_rsa README.md",
		"echo $HOME",
		"echo $PATH",
		"echo ${HOME}/bin",
		"echo $TOKENS_PER_PAGE",
		"echo $GIT_AUTHOR_NAME",
		"echo $PASSENGER_COUNT",
		"echo $KEY",
		"echo $MONKEY",
		"printenv HOME",
		"printenv PATH",
		"cat README.md",
		"cat .env.example",
		"cat .env.sample",
		"cat .env.template",
		"cat id_rsa.pub",
		"cat deploy_key.pub",
		"cat internal/secrets.go",
		"cat docs/secrets.md",
		"cat package.json",
		"cat *.md",
		"cat *",
		"ls *",
		"ls -la .env",
		"stat .env",
		"test -f .env",
		"find . -name '*.pem'",
		"find . -name .env",
		"echo .env",
		"echo id_rsa",
		"echo see credentials.json",
		"cat environment.txt",
		"cat keyboard.txt",
		"cat monkey.go",
	} {
		classifyIs(t, cmd, Safe)
	}
}

// An agent running as root (the sandbox user) has HOME=/root. /root is a
// system path for every other account, but the current user's own home must
// follow the home rules: ordinary files are local writes, rc files,
// credential directories and odek anchors still escalate.
func TestHomeIsRoot_OrdinaryWritesAreLocal(t *testing.T) {
	t.Setenv("HOME", "/root")
	for _, cmd := range []string{
		"echo x > ~/notes.txt",
		"echo x > /root/notes.txt",
		"echo x > $HOME/notes.txt",
		"echo x >> ${HOME}/notes.txt",
		"mv evil ~/.local/bin/git",
		"touch ~/a",
		"mkdir -p ~/proj/src",
		"cp a ~/b",
		"tee ~/x.log < /dev/null",
	} {
		classifyIs(t, cmd, LocalWrite)
	}
	for _, cmd := range []string{
		"cat ~/a", "cat /root/a", "ls /root", "ls ~", "head ~/notes.txt", "cat $HOME/notes.txt",
	} {
		classifyIs(t, cmd, Safe)
	}
	if got := ClassifyPath("/root/notes.txt"); got != LocalWrite {
		t.Errorf("ClassifyPath(/root/notes.txt) = %s, want local_write", got)
	}
	if got := ClassifyPath("/root"); got != LocalWrite {
		t.Errorf("ClassifyPath(/root) = %s, want local_write", got)
	}
}

func TestHomeIsRoot_ProtectedHomePathsStillEscalate(t *testing.T) {
	t.Setenv("HOME", "/root")
	for _, cmd := range []string{
		"echo x > ~/.bashrc",
		"echo x >> /root/.bashrc",
		"echo x > ~/.profile",
		"echo x > ~/.zshenv",
		"echo x > ~/.ssh/authorized_keys",
		"echo x > ~/.ssh/id_rsa",
		"cat ~/.ssh/id_rsa",
		"cat /root/.ssh/id_rsa",
		"echo x > ~/.odek/config.json",
		"cat ~/.odek/config.json",
		"cat ~/.odek/secrets.env",
		"echo x > ~/.aws/credentials",
		"cat ~/.aws/credentials",
		"echo x > ~/.config/git/config",
		"echo x > ~/.gitconfig",
		"echo x > ~/.netrc",
	} {
		got := Classify(cmd)
		if Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for _, p := range []string{"/root/.bashrc", "/root/.ssh/id_rsa", "/root/.odek/config.json", "/root/.aws/credentials"} {
		if got := ClassifyPath(p); Rank(got) < Rank(SystemWrite) {
			t.Errorf("ClassifyPath(%q) = %s, want at least system_write", p, got)
		}
	}
	// System directories and other accounts' homes keep their rules.
	for _, p := range []string{"/etc/hosts", "/usr/local/bin/x", "/var/lib/x", "/home/alice/.bashrc", "/home/alice/.ssh/id_rsa"} {
		if got := ClassifyPath(p); Rank(got) < Rank(SystemWrite) {
			t.Errorf("ClassifyPath(%q) = %s, want at least system_write", p, got)
		}
	}
	classifyIs(t, "echo x > /home/alice/notes.txt", LocalWrite)
}

// `rm -rf /root/x` and `rm -rf ~/x` name the same directory when HOME=/root,
// so they carry the same effects.
func TestHomeIsRoot_WipeTargetParity(t *testing.T) {
	t.Setenv("HOME", "/root")
	for _, pair := range [][2]string{
		{"rm -rf /root/x", "rm -rf ~/x"},
		{"rm -rf /root/x/y", "rm -rf $HOME/x/y"},
		{"rm /root/x", "rm ~/x"},
	} {
		abs, tilde := Analyze(pair[0]).Effects, Analyze(pair[1]).Effects
		if len(abs) != len(tilde) {
			t.Errorf("effects of %q = %v, of %q = %v, want equal", pair[0], abs, pair[1], tilde)
			continue
		}
		for i := range abs {
			if abs[i] != tilde[i] {
				t.Errorf("effects of %q = %v, of %q = %v, want equal", pair[0], abs, pair[1], tilde)
				break
			}
		}
	}
	classifyIs(t, "rm /root/x", LocalWrite)
	classifyIs(t, "rm -rf /root/x", Destructive)
	classifyIs(t, "rm -rf ~/x", Destructive)
	if isSystemPath("/root/x") {
		t.Errorf("isSystemPath(/root/x) = true with HOME=/root")
	}
}

// With a different current user, /root is another account's home and keeps
// the system-path rules.
func TestHomeNotRoot_RootStaysSystem(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	classifyIs(t, "echo x > /root/notes.txt", SystemWrite)
	classifyIs(t, "echo x > /root/.bashrc", Persistence)
	classifyIs(t, "echo x > ~/notes.txt", LocalWrite)
}

// A home outside /home that sits under another system prefix still gets the
// home rules, but a degenerate home (/, a bare system directory) never turns
// the system tree into local writes.
func TestHomePrecedence_ServiceHomesAndDegenerateHomes(t *testing.T) {
	t.Setenv("HOME", "/var/lib/svc")
	classifyIs(t, "echo x > /var/lib/svc/data.txt", LocalWrite)
	classifyIs(t, "echo x > ~/data.txt", LocalWrite)
	classifyIs(t, "cat /var/lib/svc/data.txt", Safe)
	classifyIs(t, "echo x > /var/lib/other/data.txt", SystemWrite)
	if got := Classify("echo x > ~/.bashrc"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("service-home rc file = %s, want at least system_write", got)
	}

	for _, home := range []string{"/", "/usr", "/etc", "/var"} {
		t.Setenv("HOME", home)
		for _, p := range []string{"/etc/hosts", "/usr/local/bin/x", "/var/lib/x"} {
			if got := ClassifyPath(p); Rank(got) < Rank(SystemWrite) {
				t.Errorf("HOME=%s: ClassifyPath(%q) = %s, want at least system_write", home, p, got)
			}
		}
	}
}
