package danger

import (
	"testing"
)

func TestRED_ReviewDynamicArgvShortClustersAndShellC(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, CodeExecution,
		`xargs -0a list sh`,
		`xargs -ta list sh`,
		`xargs -a list bash -c`,
		`parallel -a list bash -c`,
		`parallel -a list sh -c`,
		`parallel -a list`,
	)
}

func TestRED_ReviewDynamicArgvAllowlistInverted(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{"docker", "kubectl", "npm", "pip", "gh", "curl", "scp", "nc", "psql", "crontab", "sqlite3", "openssl", "gpg", "less", "man", "unzip"} {
		dangerHunt13NotAllowed(t, CodeExecution, `xargs -a list `+c)
	}
	cfg := &DangerousConfig{}
	for _, c := range []string{`xargs -a list grep`, `xargs -a list cat`, `xargs -a list wc`, `xargs -a list sha256sum`} {
		if Classify(c) != Safe || cfg.ActionForCommand(c) != Allow {
			t.Errorf("%q should stay safe/allow, got %s", c, Classify(c))
		}
	}
}

func TestRED_ReviewRunnerSpellingsUnwrapped(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{
		`bundle e rm -rf ~`,
		`pnpm -C x exec rm -rf ~`,
		`pnpm --dir x exec rm -rf ~`,
		`yarn rm -rf ~`,
		`yarn exec rm -rf ~`,
		`bun x rm -rf ~`,
		`bun --cwd x x rm -rf ~`,
		`nvm exec 18 rm -rf ~`,
		`fnm exec --using=18 rm -rf ~`,
		`pyenv exec rm -rf ~`,
		`rbenv exec rm -rf ~`,
	} {
		if got := Classify(c); got != Destructive {
			t.Errorf("Classify(%q) = %s, want destructive", c, got)
		}
	}
	dangerHunt13NotAllowed(t, CodeExecution, `pnpm exec jest`, `bun x tsc`, `pyenv exec python x.py`)
}

func TestRED_ReviewStartupFilesPersistence(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, SystemWrite,
		`echo x > ~/.ipython/profile_work/startup/00.py`,
		`echo x > ~/.ipython/profile_default/ipython_config.py`,
		`echo x > ~/.bashrc.d/x.sh`,
		`echo x > ~/.profile.d/x.sh`,
		`echo x > ~/.zshrc.d/x.zsh`,
		`echo x > ~/.pdbrc`,
		`echo x > ~/.irbrc`,
		`echo x > ~/.jupyter/jupyter_notebook_config.py`,
		`echo x > ~/.emacs.d/init.el`,
		`echo x > ~/.vim/vimrc`,
		`echo x > .vscode/tasks.json`,
	)
}

func TestRED_ReviewPackageManagerShellSettings(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, SystemWrite,
		`npm config set script-shell /tmp/x`,
		`npm set script-shell=/tmp/x`,
		`yarn config set script-shell /tmp/x`,
		`pnpm config set script-shell /tmp/x`,
		`npm config set node-options --require=/tmp/x.js`,
	)
	dangerHunt13NotAllowed(t, CodeExecution,
		`npm --script-shell id run x`,
		`npm run x --script-shell=/tmp/x`,
		`npm run x --node-options=--require=/tmp/x.js`,
	)
}

func TestRED_ReviewExtRemoteHelper(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, CodeExecution,
		`git clone 'ext::sh -c id'`,
		`git -c protocol.ext.allow=always clone 'ext::sh -c id' x`,
		`git fetch 'fd::3'`,
		`git ls-remote 'ext::sh -c id'`,
	)
}

func TestRED_ReviewPythonModuleSpellings(t *testing.T) {
	dangerHunt13LedgerSetup(t)
	for _, cmd := range []string{
		`python3 -Bm evil`,
		`python3 -um evil`,
		`python3.11 -m evil`,
		`python3 -m pkg`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want the module gated", cmd)
		}
	}
}

func TestRED_ReviewLedgerMoreWriters(t *testing.T) {
	dangerHunt13LedgerSetup(t)
	for _, cmd := range []string{
		`ar x a.a && bash x.sh`,
		`jar xf a.jar && bash x.sh`,
		`pax -r -f a.pax && bash x.sh`,
		`dpkg-deb -x a.deb . && bash x.sh`,
		`git submodule update && bash x.sh`,
		`git sparse-checkout set d && bash x.sh`,
		`git worktree add w && bash x.sh`,
		`hg update && bash x.sh`,
		`svn update && bash x.sh`,
		`ed x.sh && bash x.sh`,
		`mv d/ . && bash x.sh`,
		`cp -r d/. . && bash x.sh`,
		`rsync -a d/ . && bash x.sh`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want x.sh gated", cmd)
		}
	}
}

func TestRED_ReviewLedgerReadOnlyVariantsKeepLicence(t *testing.T) {
	dangerHunt13LedgerSetup(t)
	for _, cmd := range []string{
		`git stash list && bash x.sh`,
		`git stash show && bash x.sh`,
		`git clean -n && bash x.sh`,
		`git checkout -b feature && bash x.sh`,
		`git reset --soft HEAD~1 && bash x.sh`,
		`git switch -c feature && bash x.sh`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) != 0 {
			t.Errorf("UnreadScriptTargets(%q) = %v, want none (read-only variant)", cmd, got)
		}
	}
	for _, cmd := range []string{
		`git pull && bash x.sh`,
		`git merge other && bash x.sh`,
		`git checkout other && bash x.sh`,
		`git checkout other -- x.sh && bash x.sh`,
		`git restore x.sh && bash x.sh`,
		`git reset --hard && bash x.sh`,
		`git switch other && bash x.sh`,
		`git rebase other && bash x.sh`,
		`git stash pop && bash x.sh`,
		`git stash apply && bash x.sh`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want x.sh gated", cmd)
		}
	}
}

func TestRED_ReviewNamerefSecretNames(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, SystemWrite,
		`declare -n a=SESSION_COOKIE; echo $a`,
		`declare -n a=SLACK_WEBHOOK; echo $a`,
		`declare -n a=SSH_AUTH_SOCK; echo $a`,
		`k=KEY; declare -n a=OPENAI_API_${k}; echo $a`,
		`declare -n a="OPENAI_API_$K"; echo $a`,
	)
}
