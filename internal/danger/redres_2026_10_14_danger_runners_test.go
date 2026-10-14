package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// drRunnersAction evaluates cmd with the stock policy.
func drRunnersAction(cmd string) Action {
	return (&DangerousConfig{}).ActionForCommand(cmd)
}

// Package and project runners accept global options in front of the verb
// that runs a command (`poetry -C dir run CMD`, `npm --prefix dir exec CMD`,
// `yarn workspace NAME exec CMD`). The command they run must be classified
// as what it is, whatever global options precede the verb.
func TestRED_RunnerGlobalOptionsBeforeVerbUnwrapCommand(t *testing.T) {
	deny := []string{
		"poetry -C x run rm -rf /",
		"poetry --directory x run rm -rf /",
		"poetry --directory=x run rm -rf /",
		"poetry -P x run rm -rf /",
		"poetry --project x run rm -rf /",
		"poetry -v --no-ansi --project x run rm -rf /",
		"pipenv --python 3.12 run rm -rf /",
		"pipenv --pypi-mirror https://m.example run rm -rf /",
		"bundle --gemfile x exec rm -rf /",
		"bundle -r 3 exec rm -rf /",
		"pdm -p x run rm -rf /",
		"hatch -e test run rm -rf /",
		"uv --directory x run rm -rf /",
		"uv --project x run -- rm -rf /",
		"yarn workspace web exec rm -rf /",
		"yarn workspace web run rm -rf /",
		"yarn workspace web rm -rf /",
		"yarn workspaces foreach exec rm -rf /",
		"yarn workspaces foreach -A exec rm -rf /",
		"yarn workspaces foreach --all --jobs 4 --include web exec rm -rf /",
		"yarn workspaces foreach -A run rm -rf /",
		"yarn --cwd web run rm -rf /",
		"yarn run rm -rf /",
		"npm exec rm -rf /",
		"npm exec -- rm -rf /",
		"npm --prefix x exec rm -rf /",
		"npm -w x exec rm -rf /",
		"npm --workspace=x exec -- rm -rf /",
		"npm x rm -rf /",
		"npm x -- rm -rf /",
		"npm exec -p pkg -- rm -rf /",
		"npm exec -c 'rm -rf /'",
		"npm exec --call='rm -rf /'",
		"npm explore foo -- rm -rf /",
		"npm explore foo -- 'rm -rf /'",
		"npx rm -rf /",
		"npx -p pkg rm -rf /",
		"npx -c 'rm -rf /'",
		"fnm --log-level quiet exec rm -rf /",
		"fnm --log-level=quiet exec --using 18 rm -rf /",
	}
	for _, c := range deny {
		if got := drRunnersAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); the runner runs `rm -rf /`, want deny", c, got, Classify(c))
		}
	}
	// A global option this grammar does not know may take a value; the
	// verb behind it still runs the command.
	for _, c := range []string{
		"poetry --some-new-option val run rm -rf /",
		"npm --some-new-option val exec rm -rf /",
		"yarn --some-new-option val exec rm -rf /",
	} {
		if got := drRunnersAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); want deny", c, got, Classify(c))
		}
	}
	// Running any command is code execution even when the command is benign.
	for _, c := range []string{
		"poetry -C x run ls",
		"pipenv --python 3.12 run ls",
		"bundle --gemfile x exec ls",
		"npm --prefix x exec ls",
		"npm x ls",
		"yarn workspace web exec ls",
		"fnm --log-level quiet exec ls",
	} {
		if got := Classify(c); worstOf(got, CodeExecution) != got {
			t.Errorf("Classify(%q) = %s; a runner executing a command is at least code_execution", c, got)
		}
	}
	// Ordinary package-manager verbs keep their verdicts.
	for _, c := range []string{
		"poetry -C x show",
		"npm --prefix x ls",
		"npm config get registry",
		"yarn workspace web info",
		"yarn workspaces list",
	} {
		if got := drRunnersAction(c); got == Deny {
			t.Errorf("ActionForCommand(%q) = deny (class %s); no command runs here", c, Classify(c))
		}
	}
}

// uvx and tox exec run the command they name; bun's value-taking global
// options must not hide `bun x`.
func TestRED_MoreRunnerWrappersClassifyInnerCommand(t *testing.T) {
	for _, c := range []string{
		"uvx rm -rf /",
		"uvx --from pkg rm -rf /",
		"uvx --python 3.12 --with x rm -rf /",
		"uv tool run rm -rf /",
		"uv tool run --from pkg rm -rf /",
		"tox exec -- rm -rf /",
		"tox e -e py -- rm -rf /",
		"tox -e py exec -- rm -rf /",
		"bun --elide-lines 0 x rm -rf /",
		"bun --elide-lines=0 x rm -rf /",
		"bunx rm -rf /",
		"bunx --bun rm -rf /",
	} {
		if got := drRunnersAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); want deny", c, got, Classify(c))
		}
	}
}

// drRunnersScriptDir creates an unread evil.py in a fresh working directory
// and makes it the current directory.
func drRunnersScriptDir(t *testing.T) string {
	t.Helper()
	ResetReadLedgerForTest()
	t.Cleanup(ResetReadLedgerForTest)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(dir)
	p := filepath.Join(dir, "evil.py")
	if err := os.WriteFile(p, []byte("import os\nos.system('id')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Versioned interpreter builds (free-threaded `python3.13t`, debug builds)
// and Python's value-taking long options must gate an unread module run
// with -m exactly like `python3 -m evil`.
func TestRED_PythonVariantNamesAndLongOptionsGateModule(t *testing.T) {
	p := drRunnersScriptDir(t)
	if !redGated("python3 -m evil") {
		t.Fatalf("baseline: python3 -m evil must gate the unread %s", p)
	}
	for _, c := range []string{
		"python3.13t -m evil",
		"python3.13-debug -m evil",
		"python3.13d -m evil",
		"python3t -m evil",
		"python --check-hash-based-pycs always -m evil",
		"python3 --check-hash-based-pycs=always -m evil",
		"python3.13t evil.py",
		"python3.13-debug evil.py",
	} {
		if !redGated(c) {
			cls, tg := ClassifyScriptGate(c)
			t.Errorf("ClassifyScriptGate(%q) = %s %v; the unread %s runs, want gated", c, cls, tg, p)
		}
	}
	// A module that is read is licensed.
	RecordRead(p)
	if redGated("python3.13t -m evil") {
		t.Errorf("python3.13t -m evil gated after evil.py was read")
	}
}

// Project runners hand a script operand to the project's interpreter
// (`uv run script.py`); the unread-script gate must apply as it does for
// `python script.py`.
func TestRED_ProjectRunnerScriptOperandIsGated(t *testing.T) {
	p := drRunnersScriptDir(t)
	if !redGated("python evil.py") {
		t.Fatalf("baseline: python evil.py must gate the unread %s", p)
	}
	for _, c := range []string{
		"uv run evil.py",
		"uv run --with requests evil.py",
		"uv run -- evil.py",
		"uv --directory . run evil.py",
		"poetry run evil.py",
		"pipenv run evil.py",
		"pdm run evil.py",
		"timeout 5 uv run evil.py",
	} {
		if got := Classify(c); worstOf(got, CodeExecution) != got {
			t.Errorf("Classify(%q) = %s; want at least code_execution", c, got)
		}
		if !redGated(c) {
			cls, tg := ClassifyScriptGate(c)
			t.Errorf("ClassifyScriptGate(%q) = %s %v; the unread %s runs, want gated", c, cls, tg, p)
		}
	}
	RecordRead(p)
	if redGated("uv run evil.py") {
		t.Errorf("uv run evil.py gated after evil.py was read")
	}
}

// Maven and Gradle keep repository and signing credentials in the home
// directory; reading them is a credential read like ~/.npmrc.
func TestRED_BuildToolHomeCredentialFilesAreSensitive(t *testing.T) {
	for _, c := range []string{
		"cat ~/.m2/settings.xml",
		"cat ~/.m2/settings-security.xml",
		"cat ~/.gradle/gradle.properties",
		"grep password ~/.m2/settings.xml",
	} {
		if got := drRunnersAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); credential file read, want prompt", c, Classify(c))
		}
	}
	home, _ := os.UserHomeDir()
	for _, rel := range []string{".m2/settings.xml", ".gradle/gradle.properties"} {
		p := filepath.Join(home, rel)
		if !IsSensitiveReadPath("", p) {
			t.Errorf("IsSensitiveReadPath(%q) = false; want an @-reference refused", p)
		}
		if got := ClassifyPathWrite(p); got != SystemWrite {
			t.Errorf("ClassifyPathWrite(%q) = %s; want system_write", p, got)
		}
	}
	for _, c := range []string{
		"cat ~/.m2/repository/org/x/1.0/x-1.0.pom",
		"ls ~/.gradle/caches",
		"cat settings.xml",
		"cat gradle.properties",
	} {
		if got := drRunnersAction(c); got != Allow {
			t.Errorf("ActionForCommand(%q) = %s (class %s); not a credential file, want allow", c, got, Classify(c))
		}
	}
}

// npm config get of an auth key prints the registry credential.
func TestRED_NpmConfigGetAuthKeyIsCredentialRead(t *testing.T) {
	for _, c := range []string{
		"npm config get //registry.npmjs.org/:_authToken",
		"npm config get _authToken",
		"npm config get _auth",
		"npm config get //r.example/:_password",
		"npm get //registry.npmjs.org/:_authToken",
		"pnpm config get //registry.npmjs.org/:_authToken",
		"yarn config get npmAuthToken",
	} {
		if got := drRunnersAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s); prints a credential, want prompt", c, Classify(c))
		}
	}
	for _, c := range []string{"npm config get registry", "npm config get prefix", "yarn config get npmRegistryServer"} {
		if got := drRunnersAction(c); got != Allow {
			t.Errorf("ActionForCommand(%q) = %s (class %s); want allow", c, got, Classify(c))
		}
	}
	if packageConfigPrintsCredential("cargo", []string{"cargo", "config", "get", "_authToken"}) ||
		packageConfigPrintsCredential("npm", []string{"npm", "config"}) ||
		packageConfigPrintsCredential("yarn", []string{"yarn", "get", "npmAuthToken"}) {
		t.Errorf("packageConfigPrintsCredential matched a command that prints no credential")
	}
}

// The shapes runnerCommand reads: which words are the command and which
// invocations run none.
func TestDangerRunners_RunnerCommandShapes(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"yarn workspaces run build", "build"},
		{"yarn workspaces", ""},
		{"yarn workspaces list", ""},
		{"yarn workspace", ""},
		{"yarn workspace web", ""},
		{"yarn run -- rm -rf /", "rm -rf /"},
		{"yarn --cwd web", ""},
		{"yarn workspaces foreach -- exec ls", "ls"},
		{"npm -- exec ls", "ls"},
		{"npm -- install", ""},
		{"npm explo foo -- ls", ""},
		{"pnpm exec -c ls", ""},
		{"npm install", ""},
		{"npm exec -c ls", ""},
		{"tox exec -e py", ""},
		{"tox -e py", ""},
		{"uv tool install x", ""},
		{"uv tool run --from pkg -- ls -l", "ls -l"},
		{"poetry --unknown val run ls", "ls"},
		{"bundle --unknown val e ls", "ls"},
		{"poetry install", ""},
		{"nvm exec 18 node x.js", "node x.js"},
		{"pnpm -C dir exec -- ls", "ls"},
	}
	for _, c := range cases {
		tokens := strings.Fields(c.cmd)
		got := strings.Join(runnerCommand(commandName(tokens[0]), tokens), " ")
		if got != c.want {
			t.Errorf("runnerCommand(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
	for _, c := range []struct {
		tokens []string
		want   string
	}{
		{[]string{"npm", "explore", "foo"}, ""},
		{[]string{"npm", "explore", "foo", "--", "ls", "-l"}, "ls -l"},
		{[]string{"npm", "install"}, ""},
		{[]string{"npx", "-c", "rm -rf /", "--call=ls"}, "rm -rf /|ls"},
		{[]string{"pnpm", "exec", "ls"}, ""},
		{[]string{"pnpm", "install"}, ""},
		{[]string{"pnpm", "exec", "-c"}, ""},
		{[]string{"npm", "exp", "-c", "id", "foo", "--", "ls"}, "id|foo -- ls"},
		{[]string{"npm"}, ""},
	} {
		got := strings.Join(runnerCommandStrings(c.tokens[0], c.tokens), "|")
		if got != c.want {
			t.Errorf("runnerCommandStrings(%q) = %q, want %q", c.tokens, got, c.want)
		}
	}
	if runnerCommand("npm", []string{"npm"}) != nil {
		t.Errorf("a bare npm runs nothing")
	}
}

// drRunnersDeny asserts that every command is denied by the stock policy.
func drRunnersDeny(t *testing.T, why string, cmds ...string) {
	t.Helper()
	for _, c := range cmds {
		if got := drRunnersAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s); %s, want deny", c, got, Classify(c), why)
		}
	}
}

// npm resolves any unambiguous prefix of a command name (`npm exe` is
// `npm exec`, `npm explo` is `npm explore`).
func TestRED_NpmAbbreviatedRunnerVerbs(t *testing.T) {
	drRunnersDeny(t, "npm runs the abbreviated exec/explore verb",
		"npm exe -c 'rm -rf /'",
		"npm exe rm -rf /",
		"npm --prefix x exe -- rm -rf /",
		"npm explo foo -- rm -rf /",
		"npm explor foo -- rm -rf /",
		"npm exp foo -- rm -rf /",
	)
	if got := Classify("npm ex ls"); worstOf(got, CodeExecution) != got {
		t.Errorf("Classify(npm ex ls) = %s; an exec/explore prefix is at least code_execution", got)
	}
	if got := drRunnersAction("npm explain lodash"); got != Allow {
		t.Errorf("ActionForCommand(npm explain lodash) = %s; explain runs nothing", got)
	}
}

// A `--` before the verb ends the global options; the word after it is still
// the subcommand.
func TestRED_RunnerVerbAfterDoubleDash(t *testing.T) {
	drRunnersDeny(t, "the verb after -- runs the command",
		"npm -- exec rm -rf /",
		"npm --prefix x -- exec rm -rf /",
		"pnpm -- exec rm -rf /",
		"poetry -- run rm -rf /",
	)
}

// yarn, pnpm and bun read their own options after exec/dlx/x; the command
// behind them is classified, and `pnpm exec -c` runs a shell command line.
func TestRED_RunnerPostVerbOptions(t *testing.T) {
	drRunnersDeny(t, "the runner runs rm -rf /",
		"yarn dlx -p pkg rm -rf /",
		"yarn dlx --package pkg rm -rf /",
		"yarn dlx -q rm -rf /",
		"yarn exec -- rm -rf /",
		"yarn workspace web dlx -p pkg rm -rf /",
		"pnpm dlx -p pkg rm -rf /",
		"pnpm dlx --package=pkg rm -rf /",
		"pnpm dlx --package pkg rm -rf /",
		"pnpm exec -r rm -rf /",
		"pnpm exec --parallel rm -rf /",
		"pnpm exec --resume-from pkg rm -rf /",
		"pnpm exec -c 'rm -rf /'",
		"pnpm exec --shell-mode 'rm -rf /'",
		"pnpm dlx -c 'rm -rf /'",
		"bun -c x x rm -rf /",
		"bun --config x x rm -rf /",
	)
}

// drRunnersDirSetup makes a working directory whose evil.py, evil.sh and
// sub/evil.py were read, and a subdirectory x holding unread copies.
func drRunnersDirSetup(t *testing.T) {
	t.Helper()
	ResetReadLedgerForTest()
	t.Cleanup(ResetReadLedgerForTest)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(dir)
	for _, base := range []string{dir, filepath.Join(dir, "x")} {
		for _, rel := range []string{"evil.py", "evil.sh", "sub/evil.py"} {
			p := filepath.Join(base, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("import os\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if base == dir {
				RecordRead(p)
			}
		}
	}
}

// A runner's directory option moves the working directory of everything it
// runs: the script, and the operands of an interpreter it starts.
func TestRED_RunnerDirectoryAppliesToScripts(t *testing.T) {
	drRunnersDirSetup(t)
	if redGated("python evil.py") {
		t.Fatalf("baseline: ./evil.py was read and must not gate")
	}
	for _, c := range []string{
		"uv --directory x run evil.py",
		"uv run --directory x evil.py",
		"uv run --directory=x evil.py",
		"uv --directory x run sub/evil.py",
		"uv --directory x run ./evil.py",
		"uv --directory x run python evil.py",
		"uv run --directory x python evil.py",
		"poetry -C x run python evil.py",
		"poetry --directory x run sh evil.sh",
		"poetry -C x run evil.py",
	} {
		if !redGated(c) {
			cls, tg := ClassifyScriptGate(c)
			t.Errorf("ClassifyScriptGate(%q) = %s %v; x/ holds the unread script, want gated", c, cls, tg)
		}
	}
	// --project selects the project, not the working directory.
	if redGated("uv --project x run evil.py") {
		t.Errorf("uv --project x run evil.py gated; the script is ./evil.py, which was read")
	}
	// A directory the analysis cannot resolve leaves the script unjudged.
	if got := Classify("uv --directory $D run evil.py"); got != Unknown {
		t.Errorf("Classify(uv --directory $D run evil.py) = %s; want unknown", got)
	}
}

// pdm run options that take a value must not be read as the script.
func TestRED_PdmRunOptionsBeforeScript(t *testing.T) {
	p := drRunnersScriptDir(t)
	for _, c := range []string{
		"pdm run --venv in-project evil.py",
		"pdm run -s evil.py",
		"pdm run -v --venv x evil.py",
	} {
		if !redGated(c) {
			cls, tg := ClassifyScriptGate(c)
			t.Errorf("ClassifyScriptGate(%q) = %s %v; the unread %s runs, want gated", c, cls, tg, p)
		}
	}
}
