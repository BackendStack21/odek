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
		{"npm -- exec ls", ""},
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
