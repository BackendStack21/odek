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
