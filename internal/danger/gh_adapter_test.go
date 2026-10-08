package danger

import "testing"

// The gh adapter classifies by command and verb, like git's. Reads of the
// GitHub API stay network_egress (allowed); remote mutation is system_write
// (prompt); irreversible remote deletion is destructive (deny); verbs that run
// local programs are code_execution; credential disclosure is system_write;
// an unrecognised command or verb is unknown (deny). Meta invocations that
// only print help or a version are safe.

type ghCase struct {
	cmd  string
	want RiskClass
}

func runGHCases(t *testing.T, cases []ghCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := Classify(tc.cmd); got != tc.want {
				t.Errorf("Classify(%q) = %s, want %s", tc.cmd, got, tc.want)
			}
		})
	}
}

// ghHasEffect reports whether the independent effects of cmd include cls.
func ghHasEffect(cmd string, cls RiskClass) bool {
	for _, e := range Analyze(cmd).Effects {
		if e == cls {
			return true
		}
	}
	return false
}

func TestGH_ReadOnlyStaysNetworkEgress(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh pr list", NetworkEgress},
		{"gh pr ls --state all", NetworkEgress},
		{"gh pr view 12 --comments", NetworkEgress},
		{"gh pr status", NetworkEgress},
		{"gh pr checks 12", NetworkEgress},
		{"gh pr diff 12", NetworkEgress},
		{"gh issue list --label bug", NetworkEgress},
		{"gh issue view 3", NetworkEgress},
		{"gh issue status", NetworkEgress},
		{"gh repo view owner/repo", NetworkEgress},
		{"gh repo list owner", NetworkEgress},
		{"gh repo clone owner/repo", NetworkEgress},
		{"gh repo clone owner/repo checkout-dir -- --depth 1", NetworkEgress},
		{"gh repo gitignore list", NetworkEgress},
		{"gh repo license view mit", NetworkEgress},
		{"gh repo deploy-key list", NetworkEgress},
		{"gh repo autolink list", NetworkEgress},
		{"gh run list", NetworkEgress},
		{"gh run view 99 --log", NetworkEgress},
		{"gh run watch 99", NetworkEgress},
		{"gh workflow list", NetworkEgress},
		{"gh workflow view ci.yml", NetworkEgress},
		{"gh release list", NetworkEgress},
		{"gh release view v1.2.3", NetworkEgress},
		{"gh release download v1.2.3", NetworkEgress},
		{"gh release verify v1.2.3", NetworkEgress},
		{"gh gist list", NetworkEgress},
		{"gh gist view abc123", NetworkEgress},
		{"gh gist clone abc123", NetworkEgress},
		{"gh search repos golang", NetworkEgress},
		{"gh search issues is:open", NetworkEgress},
		{"gh search code foo --owner bar", NetworkEgress},
		{"gh browse", NetworkEgress},
		{"gh status", NetworkEgress},
		{"gh org list", NetworkEgress},
		{"gh label list", NetworkEgress},
		{"gh cache list", NetworkEgress},
		{"gh project list --owner o", NetworkEgress},
		{"gh project view 1 --owner o", NetworkEgress},
		{"gh project item-list 1 --owner o", NetworkEgress},
		{"gh project field-list 1 --owner o", NetworkEgress},
		{"gh codespace list", NetworkEgress},
		{"gh codespace view", NetworkEgress},
		{"gh codespace ports", NetworkEgress},
		{"gh ruleset list", NetworkEgress},
		{"gh ruleset view 4", NetworkEgress},
		{"gh ruleset check main", NetworkEgress},
		{"gh variable list", NetworkEgress},
		{"gh variable get NAME", NetworkEgress},
		{"gh secret list", NetworkEgress},
		{"gh auth status", NetworkEgress},
		{"gh auth status --hostname github.com", NetworkEgress},
		{"gh config get git_protocol", NetworkEgress},
		{"gh config list", NetworkEgress},
		{"gh alias list", NetworkEgress},
		{"gh extension list", NetworkEgress},
		{"gh extension browse", NetworkEgress},
		{"gh extension search copilot", NetworkEgress},
		{"gh ssh-key list", NetworkEgress},
		{"gh gpg-key list", NetworkEgress},
		{"gh attestation verify artifact.tgz -R owner/repo", NetworkEgress},
		{"/usr/local/bin/gh pr checks", NetworkEgress},
	})
}

// The verbs keep their class whichever way the global repo/host option is
// spelled, and whether it precedes the command, sits between command and
// verb, or trails the verb.
func TestGH_GlobalFlagSpellings(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh -R owner/repo pr list", NetworkEgress},
		{"gh -Rowner/repo pr list", NetworkEgress},
		{"gh --repo owner/repo pr list", NetworkEgress},
		{"gh --repo=owner/repo pr list", NetworkEgress},
		{"gh --hostname ghe.example.com pr list", NetworkEgress},
		{"gh --hostname=ghe.example.com pr list", NetworkEgress},
		{"gh -R owner/repo --hostname ghe.example.com pr view 3", NetworkEgress},
		{"gh pr -R owner/repo list", NetworkEgress},
		{"gh pr --repo=owner/repo list", NetworkEgress},
		{"gh pr list -R owner/repo", NetworkEgress},
		{"gh issue list -- topic", NetworkEgress},

		{"gh -R owner/repo pr merge 3", SystemWrite},
		{"gh -Rowner/repo pr merge 3", SystemWrite},
		{"gh --repo=owner/repo pr merge 3", SystemWrite},
		{"gh --hostname ghe.example.com pr merge 3", SystemWrite},
		{"gh --hostname=ghe.example.com pr merge 3", SystemWrite},
		{"gh pr -R owner/repo merge 3", SystemWrite},
		{"gh pr --repo owner/repo merge 3", SystemWrite},
		{"gh -R owner/repo repo delete owner/repo --yes", Destructive},
		{"gh --repo=owner/repo release delete v1 --yes", Destructive},
		{"gh --hostname h auth token", SystemWrite},
		{"gh -R owner/repo codespace ssh", CodeExecution},
		// The option value is a value, never the command.
		{"gh -R merge pr list", NetworkEgress},
		{"gh --hostname delete pr list", NetworkEgress},
	})
}

func TestGH_RemoteMutationIsSystemWrite(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh pr create --fill", SystemWrite},
		{"gh pr new --fill", SystemWrite},
		{"gh pr merge 5 --squash", SystemWrite},
		{"gh pr close 5", SystemWrite},
		{"gh pr reopen 5", SystemWrite},
		{"gh pr edit 5 --title x", SystemWrite},
		{"gh pr review 5 --approve", SystemWrite},
		{"gh pr comment 5 --body hi", SystemWrite},
		{"gh pr ready 5", SystemWrite},
		{"gh pr checkout 5", SystemWrite},
		{"gh pr lock 5", SystemWrite},
		{"gh pr unlock 5", SystemWrite},
		{"gh pr update-branch 5", SystemWrite},
		{"gh pr revert 5", SystemWrite},
		{"gh issue create --title x --body y", SystemWrite},
		{"gh issue close 3", SystemWrite},
		{"gh issue reopen 3", SystemWrite},
		{"gh issue edit 3 --add-label bug", SystemWrite},
		{"gh issue comment 3 --body hi", SystemWrite},
		{"gh issue comment 3 -b --help", SystemWrite},
		{"gh issue transfer 3 owner/other", SystemWrite},
		{"gh issue pin 3", SystemWrite},
		{"gh issue unpin 3", SystemWrite},
		{"gh issue lock 3", SystemWrite},
		{"gh issue unlock 3", SystemWrite},
		{"gh issue develop 3 --checkout", SystemWrite},
		{"gh repo create owner/new --private", SystemWrite},
		{"gh repo fork owner/repo", SystemWrite},
		{"gh repo edit --description x", SystemWrite},
		{"gh repo rename newname", SystemWrite},
		{"gh repo sync", SystemWrite},
		{"gh repo archive owner/repo --yes", SystemWrite},
		{"gh repo unarchive owner/repo --yes", SystemWrite},
		{"gh repo set-default owner/repo", SystemWrite},
		{"gh repo deploy-key add key.pub", SystemWrite},
		{"gh repo autolink create REF http://x/<num>", SystemWrite},
		{"gh release create v2 --notes x", SystemWrite},
		{"gh release edit v2 --draft=false", SystemWrite},
		{"gh release upload v2 dist.tgz", SystemWrite},
		{"gh run cancel 9", SystemWrite},
		{"gh run rerun 9", SystemWrite},
		{"gh workflow run ci.yml", SystemWrite},
		{"gh workflow enable ci.yml", SystemWrite},
		{"gh workflow disable ci.yml", SystemWrite},
		{"gh gist create notes.txt", SystemWrite},
		{"gh gist edit abc", SystemWrite},
		{"gh label create bug", SystemWrite},
		{"gh label edit bug --color f00", SystemWrite},
		{"gh label clone owner/other", SystemWrite},
		{"gh project create --owner o --title t", SystemWrite},
		{"gh project edit 1 --owner o --title t", SystemWrite},
		{"gh project item-add 1 --owner o --url u", SystemWrite},
		{"gh project item-edit --id i", SystemWrite},
		{"gh project field-create 1 --owner o --name n", SystemWrite},
		{"gh project link 1 --owner o", SystemWrite},
		{"gh project close 1 --owner o", SystemWrite},
		{"gh project mark-template 1 --owner o", SystemWrite},
		{"gh variable set NAME --body v", SystemWrite},
		{"gh secret set NAME --body v", SystemWrite},
		{"gh codespace create -R owner/repo", SystemWrite},
		{"gh codespace stop", SystemWrite},
		{"gh codespace rebuild", SystemWrite},
		{"gh codespace edit -d name", SystemWrite},
		{"gh ssh-key add key.pub", SystemWrite},
		{"gh gpg-key add key.asc", SystemWrite},
		{"gh config set git_protocol ssh", SystemWrite},
		{"gh alias set co 'pr checkout'", SystemWrite},
		{"gh alias delete co", SystemWrite},
		{"gh attestation download artifact.tgz -R owner/repo", SystemWrite},
		{"gh extension remove name", SystemWrite},
	})
}

func TestGH_APIMethodsAndBodies(t *testing.T) {
	runGHCases(t, []ghCase{
		// Reads.
		{"gh api /user", NetworkEgress},
		{"gh api repos/o/r/pulls --paginate", NetworkEgress},
		{"gh api -X GET /user", NetworkEgress},
		{"gh api -XGET /user", NetworkEgress},
		{"gh api --method GET /user", NetworkEgress},
		{"gh api --method=get /user", NetworkEgress},
		{"gh api -X GET search/issues -f q=bug", NetworkEgress},
		{"gh api /user --jq .login", NetworkEgress},
		{"gh api graphql -f query='query { viewer { login } }'", NetworkEgress},
		// A flag value that merely looks like a method is not one.
		{"gh api /user -H 'X-Note: -X POST'", NetworkEgress},

		// Writes by method or by body flag.
		{"gh api -X POST repos/o/r/issues", SystemWrite},
		{"gh api -XPOST repos/o/r/issues", SystemWrite},
		{"gh api -X=POST repos/o/r/issues", SystemWrite},
		{"gh api --method POST repos/o/r/issues", SystemWrite},
		{"gh api --method=post repos/o/r/issues", SystemWrite},
		{"gh api repos/o/r/issues --method PATCH", SystemWrite},
		{"gh api -X PUT repos/o/r/topics", SystemWrite},
		{"gh api repos/o/r/issues -f title=x", SystemWrite},
		{"gh api repos/o/r/issues -F title=x", SystemWrite},
		{"gh api repos/o/r/issues --field title=x", SystemWrite},
		{"gh api repos/o/r/issues --raw-field title=x", SystemWrite},
		{"gh api repos/o/r/issues -ftitle=x", SystemWrite},
		{"gh api repos/o/r/issues --input body.json", SystemWrite},
		{"gh api -X GET repos/o/r/issues --input body.json", SystemWrite},
		{"gh api repos/o/r/contents/README.md -X PUT -f message=m -f content=aGk=", SystemWrite},
		{"gh api repos/o/r/contents/.github/workflows/ci.yml -f message=m -f content=aGk=", SystemWrite},
		{"gh api graphql -f query='mutation { addStar(input:{starrableId:\"x\"}) { clientMutationId } }'", SystemWrite},
		{"gh api graphql -f query='MUTATION { x }'", SystemWrite},
		{"gh api /graphql -F query=@change.graphql", SystemWrite},
		{"gh api graphql --input q.json", SystemWrite},

		// Deletion.
		{"gh api -X DELETE repos/o/r", Destructive},
		{"gh api -XDELETE repos/o/r/issues/comments/1", Destructive},
		{"gh api --method DELETE repos/o/r", Destructive},
		{"gh api --method=DELETE repos/o/r", Destructive},
		{"gh api repos/o/r -X delete", Destructive},
	})
}

func TestGH_DestructiveVerbs(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh repo delete owner/repo --yes", Destructive},
		{"gh release delete v1 --yes", Destructive},
		{"gh release delete-asset v1 asset.zip", Destructive},
		{"gh gist delete abc", Destructive},
		{"gh issue delete 3 --yes", Destructive},
		{"gh run delete 9", Destructive},
		{"gh cache delete --all", Destructive},
		{"gh project delete 1 --owner o", Destructive},
		{"gh project item-delete 1 --id i", Destructive},
		{"gh project field-delete --id f", Destructive},
		{"gh label delete bug --yes", Destructive},
		{"gh secret delete NAME", Destructive},
		{"gh secret remove NAME", Destructive},
		{"gh variable delete NAME", Destructive},
		{"gh ssh-key delete 12 --yes", Destructive},
		{"gh gpg-key delete 12 --yes", Destructive},
		{"gh codespace delete --all", Destructive},
		{"gh repo deploy-key delete 4", Destructive},
		{"gh repo autolink delete 4", Destructive},
	})
}

func TestGH_CredentialDisclosureAndAuthChanges(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh auth token", SystemWrite},
		{"gh auth token --hostname github.com", SystemWrite},
		{"gh auth status --show-token", SystemWrite},
		{"gh auth status -t", SystemWrite},
		{"gh auth status -at", SystemWrite},
		{"gh auth login", SystemWrite},
		{"gh auth login --with-token", SystemWrite},
		{"gh auth logout", SystemWrite},
		{"gh auth refresh -s repo", SystemWrite},
		{"gh auth setup-git", SystemWrite},
		{"gh auth switch", SystemWrite},
		{"gh config get oauth_token", SystemWrite},
		{"gh config list --host github.com", NetworkEgress},
	})
}

func TestGH_LocalCodeExecution(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh extension install owner/gh-ext", CodeExecution},
		{"gh extension upgrade --all", CodeExecution},
		{"gh extension exec name", CodeExecution},
		{"gh extension create name", CodeExecution},
		{"gh ext install owner/gh-ext", CodeExecution},
		{"gh extensions install owner/gh-ext", CodeExecution},
		{"gh alias set deploy '!make deploy'", CodeExecution},
		{"gh alias set deploy --shell 'make deploy'", CodeExecution},
		{"gh alias set -s deploy 'make deploy'", CodeExecution},
		{"gh alias set --clobber deploy '!make deploy'", CodeExecution},
		{"gh alias import aliases.yml", CodeExecution},
		{"gh alias import -", CodeExecution},
		{"gh codespace ssh", CodeExecution},
		{"gh cs ssh -c name -- ls", CodeExecution},
		{"gh codespace code", CodeExecution},
		{"gh codespace cp -e local remote:path", CodeExecution},
		{"gh codespace ports forward 80:80", CodeExecution},
		{"gh codespace logs", CodeExecution},
		{"gh codespace jupyter", CodeExecution},
		{"gh copilot", CodeExecution},
		{"gh copilot suggest 'list files'", CodeExecution},
		{"gh config set editor 'vim -c !sh'", CodeExecution},
		{"gh config set pager 'sh -c evil'", CodeExecution},
		{"gh config set browser evil", CodeExecution},
		// git flags after -- are handed to git clone.
		{"gh repo clone owner/repo -- --upload-pack=/tmp/x", CodeExecution},
		{"gh repo clone owner/repo dir -- -c core.fsmonitor=/tmp/x", CodeExecution},
		{"gh gist clone abc dir -- --config core.sshCommand=/tmp/x", CodeExecution},
	})
}

func TestGH_UnrecognisedIsUnknown(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh frobnicate", Unknown},
		{"gh pr frobnicate", Unknown},
		{"gh pr merge-all", Unknown},
		{"gh repo deploy-key frobnicate", Unknown},
		{"gh release frobnicate v1", Unknown},
		{"gh agent-task create", Unknown},
		{"gh some-installed-extension arg", Unknown},
		// An option the command resolver does not know cannot be told apart
		// from a flag-with-value, so it never selects a read-only verb.
		{"gh --foo pr list", Unknown},
		{"gh pr --squash view merge", Unknown},
		{"gh pr --draft list", Unknown},
		{"gh pr -- list", Unknown},
		{"gh pr 123", Unknown},
		{"gh api-ish", Unknown},
		{"gh $CMD list", Unknown},
		{"gh pr $VERB 5", Unknown},
	})
}

func TestGH_MetaInvocationsAreSafe(t *testing.T) {
	runGHCases(t, []ghCase{
		{"gh", Safe},
		{"gh --version", Safe},
		{"gh --help", Safe},
		{"gh -h", Safe},
		{"gh help", Safe},
		{"gh help pr", Safe},
		{"gh version", Safe},
		{"gh completion -s bash", Safe},
		{"gh -R owner/repo help", Safe},
		{"gh pr", Safe},
		{"gh pr --help", Safe},
		{"gh repo", Safe},
		{"gh auth", Safe},
	})
}

// run/release download write files; -D/--dir and -O/--output name where. The
// destination goes through the write-target rules, so a download aimed at a
// credential directory, shell rc file or hook directory escalates.
func TestGH_DownloadDestinations(t *testing.T) {
	allow := []string{
		"gh run download 123",
		"gh run download 123 -D artifacts",
		"gh run download 123 --dir=artifacts",
		"gh release download v1 -D dist",
		"gh release download v1 --output notes.txt",
		"gh release download v1 -O -",
		"gh repo clone owner/repo",
		"gh repo clone owner/repo local-dir",
		"gh gist clone abc local-dir",
	}
	for _, c := range allow {
		if got := wrAction(c); got != Allow {
			t.Errorf("ActionForCommand(%q) = %s (class %s), want allow", c, got, Classify(c))
		}
	}
	if !ghHasEffect("gh run download 123", LocalWrite) {
		t.Errorf("gh run download should carry a local_write effect")
	}
	if !ghHasEffect("gh release download v1 -D dist", LocalWrite) {
		t.Errorf("gh release download should carry a local_write effect")
	}

	escalated := []string{
		"gh run download 123 -D ~/.ssh",
		"gh run download 123 -D~/.ssh",
		"gh run download 123 --dir ~/.ssh",
		"gh run download 123 --dir=~/.ssh",
		"gh run download 123 -D .git/hooks",
		"gh run download 123 --dir=.git/hooks",
		"gh release download v1 -D /etc/cron.d",
		"gh release download v1 --dir=/etc/cron.d",
		"gh release download v1 -O ~/.bashrc",
		"gh release download v1 -O~/.bashrc",
		"gh release download v1 --output ~/.bashrc",
		"gh release download v1 --output=~/.zshrc",
		"gh repo clone owner/repo ~/.ssh",
		"gh repo clone owner/repo .git/hooks",
		"gh repo clone owner/repo /etc/cron.d",
		"gh repo clone owner/repo ~/.config/systemd/user",
		"gh gist clone abc ~/.ssh",
		"gh gist clone abc .git/hooks",
		"gh run download 123 -D \"$TARGET\"",
		"gh repo clone owner/repo \"$TARGET\"",
	}
	for _, c := range escalated {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s), want prompt or deny for a sensitive destination", c, Classify(c))
		}
	}
}

// Compound commands keep every effect: a prompt or deny sibling is not
// laundered by a read.
func TestGH_PolicyActionsAndCompounds(t *testing.T) {
	tests := []struct {
		cmd  string
		want Action
	}{
		{"gh pr list", Allow},
		{"gh pr view 1 | head -5", Allow},
		{"gh api /user", Allow},
		{"gh pr merge 1", Prompt},
		{"gh auth token", Prompt},
		{"gh extension install owner/gh-x", Prompt},
		{"gh api -X POST repos/o/r/issues", Prompt},
		{"gh repo delete owner/repo --yes", Deny},
		{"gh api -X DELETE repos/o/r", Deny},
		{"gh pr frobnicate", Deny},
		{"gh pr list && gh repo delete owner/repo --yes", Deny},
		{"gh pr list; gh auth token", Prompt},
		{"echo hi | gh frobnicate", Deny},
		{"gh auth token | cat", Prompt},
		{"sudo gh repo delete owner/repo --yes", Deny},
		{"env GH_HOST=h gh repo delete owner/repo --yes", Deny},
		{"nohup gh release delete v1 --yes", Deny},
		{"bash -c 'gh repo delete owner/repo --yes'", Deny},
		{"echo $(gh auth token)", Prompt},
	}
	for _, tc := range tests {
		if got := wrAction(tc.cmd); got != tc.want {
			t.Errorf("ActionForCommand(%q) = %s (class %s), want %s", tc.cmd, got, Classify(tc.cmd), tc.want)
		}
	}
}

// Without -D/-O the files land in the working directory, so a preceding cd
// into a sensitive directory carries the write with it.
func TestGH_DownloadIntoChangedDirectory(t *testing.T) {
	for _, c := range []string{
		"cd /etc && gh run download 1",
		"cd /etc && gh release download v1",
		"cd /etc/cron.d && gh repo clone owner/repo",
		"cd ~/.ssh && gh gist clone abc",
	} {
		if got := wrAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s), want prompt or deny", c, Classify(c))
		}
	}
	if got := wrAction("cd /tmp && gh run download 1"); got != Allow {
		t.Errorf("ActionForCommand(cd /tmp && gh run download 1) = %s, want allow", got)
	}
}
