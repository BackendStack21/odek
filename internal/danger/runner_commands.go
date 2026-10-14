package danger

import "strings"

// Runner option grammars: the value-taking global options that may sit
// between the runner and its subcommand.
var (
	pnpmRunnerSpec = optSpec{posix: true, short: "CF", long: longTable("dir filter reporter loglevel workspace-concurrency", "")}
	bunRunnerSpec  = optSpec{posix: true, long: longTable("cwd config", "")}
	yarnRunnerSpec = optSpec{posix: true, long: longTable("cwd", "")}
	fnmRunnerSpec  = optSpec{posix: true, long: longTable("using", "")}
)

// yarnSubcommands are the yarn verbs that are not a bin or script name; any
// other first operand makes yarn run that command.
var yarnSubcommands = fieldSet("add bin cache check config create dedupe dlx exec explain generate-lock-entry " +
	"global help import info init install licenses link list login logout node npm outdated owner pack patch " +
	"plugin policies publish rebuild remove run set search tag team test unlink unplug up upgrade " +
	"upgrade-interactive version versions why workspace workspaces constraints start build lint " +
	"audit access autoclean patch-commit stage")

// yarnRunsBin reports whether a yarn invocation runs a binary explicitly
// (`yarn exec|dlx|node CMD`). A bare `yarn WORD` is a script or bin name, which
// npm run and pnpm treat the same way, so it keeps the verdict of the
// command it names without an extra floor.
func yarnRunsBin(tokens []string) bool {
	r := yarnRunnerSpec.parse(tokens[1:])
	if r.operandAt < 0 {
		return false
	}
	switch tokens[1+r.operandAt] {
	case "exec", "dlx", "node":
		return true
	}
	return false
}

// runnerCommand returns the command a package or version manager runs on
// behalf of its caller (`pnpm -C x exec CMD`, `yarn CMD`, `bun x CMD`,
// `nvm exec 18 CMD`, `fnm exec CMD`, `pyenv exec CMD`, `rbenv exec CMD`), or
// nil. Script names (`yarn run build`) are not commands and are not returned.
func runnerCommand(name string, tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	after := func(spec optSpec, verbs ...string) []string {
		r := spec.parse(tokens[1:])
		if r.operandAt < 0 {
			return nil
		}
		verb := tokens[1+r.operandAt]
		for _, v := range verbs {
			if verb == v {
				rest := tokens[1+r.operandAt+1:]
				if len(rest) > 0 && rest[0] == "--" {
					rest = rest[1:]
				}
				return rest
			}
		}
		return nil
	}
	switch name {
	case "pnpm":
		return after(pnpmRunnerSpec, "exec", "dlx", "x")
	case "bun":
		rest := after(bunRunnerSpec, "x")
		// bunx options (--bun, -p/--package PKG) precede the command.
		if r := (optSpec{posix: true, short: "p", long: longTable("package", "")}).parse(rest); r.operandAt >= 0 {
			return rest[r.operandAt:]
		} else if len(r.rest) > 0 {
			return r.rest
		}
		return nil
	case "pyenv", "rbenv":
		return after(optSpec{posix: true}, "exec")
	case "fnm":
		rest := after(fnmRunnerSpec, "exec")
		if r := fnmRunnerSpec.parse(rest); r.operandAt >= 0 {
			return rest[r.operandAt:]
		} else if len(r.rest) > 0 {
			return r.rest
		}
		return nil
	case "nvm":
		// nvm exec [--silent] [VERSION] CMD: skip the flags and the version.
		rest := after(optSpec{posix: true}, "exec", "run")
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			return rest[1:] // the version
		}
	case "yarn":
		r := yarnRunnerSpec.parse(tokens[1:])
		if r.operandAt < 0 {
			return nil
		}
		verb := tokens[1+r.operandAt]
		rest := tokens[1+r.operandAt+1:]
		switch {
		case verb == "exec" || verb == "dlx" || verb == "node":
			return rest
		case yarnSubcommands[verb]:
			return nil
		}
		return tokens[1+r.operandAt:]
	}
	return nil
}
