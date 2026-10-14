package danger

import "testing"

func TestRED_Review2PythonModuleFollowedByOptions(t *testing.T) {
	dangerHunt13LedgerSetup(t)
	for _, cmd := range []string{
		`python3 -m evil -c x`,
		`python3 -m evil -m other`,
		`python3 -Bm evil --flag`,
		`python3 -m pkg -W x`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want the module gated", cmd)
		}
	}
}

func TestRED_Review2GitExtViaConfig(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, CodeExecution,
		`git -c protocol.ext.allow=always -c 'url.ext::sh -c id %S.insteadOf=https://x/' clone https://x/y`,
		`git -c 'url.ext::sh -c id %S.pushInsteadOf=https://x/' push`,
		`git -c 'url.fd::3/.insteadOf=https://x/' fetch`,
		`git config url.ext::sh.insteadOf https://x/`,
		`git -c protocol.ext.allow=always clone https://x/y`,
		`git -c protocol.allow=always clone https://x/y`,
	)
}

func TestRED_Review2ParallelHeaderOption(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{
		`parallel --header : sh -c 'rm -rf ~' ::: a`,
		`parallel --colsep , sh -c 'rm -rf ~' ::: a`,
		`parallel -j4 --header : rm -rf ~ ::: a`,
		`parallel -S host --header : rm -rf ~ ::: a`,
	} {
		if got := Classify(c); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", c, got)
		}
	}
	for _, c := range []string{`parallel --header : rm -rf ~ ::: a`, `parallel -j4 --header : sh -c 'rm -rf ~' ::: a`} {
		if got := Classify(c); got != Destructive {
			t.Errorf("Classify(%q) = %s, want destructive", c, got)
		}
	}
}

func TestRED_Review2PackageManagerSetKeys(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, SystemWrite,
		`npm config set --location global script-shell /tmp/x`,
		`npm config set -L global script-shell /tmp/x`,
		`npm set loglevel=warn script-shell=/tmp/x`,
		`pnpm config set --location global script-shell x`,
		`npm config set loglevel warn node-options=--require=/tmp/x.js`,
	)
}

func TestRED_Review2LedgerForcedCheckoutAndMergeAbort(t *testing.T) {
	dangerHunt13LedgerSetup(t)
	for _, cmd := range []string{
		`git checkout -f -b feature && bash x.sh`,
		`git checkout --force -b feature && bash x.sh`,
		`git checkout -B feature && bash x.sh`,
		`git checkout -fb feature && bash x.sh`,
		`git switch -c feature -f && bash x.sh`,
		`git switch --discard-changes -c feature && bash x.sh`,
		`git merge --abort && bash x.sh`,
	} {
		if got := UnreadScriptTargets(cmd); len(got) == 0 {
			t.Errorf("UnreadScriptTargets(%q) = [], want x.sh gated", cmd)
		}
	}
	for _, cmd := range []string{`git checkout -b feature && bash x.sh`, `git switch -c feature && bash x.sh`} {
		if got := UnreadScriptTargets(cmd); len(got) != 0 {
			t.Errorf("UnreadScriptTargets(%q) = %v, want none", cmd, got)
		}
	}
}

func TestRED_Review2DynamicArgvAllowlistShrunk(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{"sed", "sort", "uniq", "file"} {
		dangerHunt13NotAllowed(t, CodeExecution, `xargs -a list `+c)
	}
}

func TestRED_Review2YarnScriptsMatchNpmAndPnpm(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	pnpm := Classify("pnpm dev")
	for _, w := range []string{"dev", "serve", "format", "typecheck"} {
		if got := Classify("yarn " + w); got != pnpm {
			t.Errorf("Classify(yarn %s) = %s, want %s (as pnpm %s)", w, got, pnpm, w)
		}
	}
	for _, c := range []string{"yarn audit", "yarn info x", "yarn why x", "yarn licenses list", "yarn outdated", "yarn list", "yarn cache list", "yarn config get registry", "yarn access ls", "yarn autoclean", "yarn patch-commit x", "yarn stage"} {
		if got := Classify(c); got != Classify("yarn install") && got != Safe && got != Install {
			t.Errorf("Classify(%q) = %s, want a non-prompting builtin verdict", c, got)
		}
	}
	if got := Classify("yarn rm -rf ~"); got != Destructive {
		t.Errorf("Classify(yarn rm -rf ~) = %s, want destructive", got)
	}
}
