package danger

import (
	"slices"
	"strings"
)

// Runner option grammars: the value-taking global options that may sit
// between the runner and its subcommand.
var (
	pnpmRunnerSpec = optSpec{posix: true, short: "CF", long: longTable("dir filter reporter loglevel workspace-concurrency", "")}
	bunRunnerSpec  = optSpec{posix: true, short: "rdlF", long: longTable("cwd config elide-lines env-file preload "+
		"tsconfig-override define loader conditions filter main-fields port origin jsx-factory jsx-fragment "+
		"jsx-import-source jsx-runtime extension-order inspect-wait", "")}
	yarnRunnerSpec = optSpec{posix: true, long: longTable("cwd", "")}
	fnmRunnerSpec  = optSpec{posix: true, long: longTable("using log-level arch version-file-strategy "+
		"node-dist-mirror fnm-dir shell", "")}
	// yarnForeachSpec is `yarn workspaces foreach`'s option grammar.
	yarnForeachSpec = optSpec{posix: true, short: "j", long: longTable("jobs include exclude from", "")}
	// npmGlobalSpec lists npm's value-taking options that may precede the
	// command (`npm --prefix dir exec …`, `npm -w pkg x …`).
	npmGlobalSpec = optSpec{posix: true, short: "Cw", long: longTable("prefix workspace registry userconfig "+
		"globalconfig cache loglevel scope tag otp include omit before location", "")}
	// npmExecSpec is the grammar of `npm exec` and npx: -p/--package names
	// a package to install, -c/--call carries a shell command line.
	npmExecSpec = optSpec{posix: true, short: "pcw", long: longTable("package call workspace prefix registry "+
		"userconfig globalconfig cache loglevel scope", "")}
	// uvToolRunSpec is `uv tool run` and uvx: `uv run`'s grammar plus
	// --from PKG.
	uvToolRunSpec = optSpec{posix: true, short: uvRunSpec.short, long: withLong(uvRunSpec.long, "from")}
	// toxSpec lists tox's value-taking options.
	toxSpec = optSpec{posix: true, short: "ecx", long: longTable("conf root workdir override colored "+
		"exit-and-dump-after runner installpkg discover hash-seed", "")}
)

// withLong returns a copy of table with more value-taking long options.
func withLong(table map[string]bool, names ...string) map[string]bool {
	out := make(map[string]bool, len(table)+len(names))
	for k, v := range table {
		out[k] = v
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// runnerVerbAt returns the index in args of the runner verb that runs a
// command, or -1. The first operand under the runner's grammar is the verb.
// When it is not one of verbs, the arguments are read again as options
// that may each take the following word as a value, so a global option the
// grammar does not know cannot hide the verb behind its value.
func runnerVerbAt(spec optSpec, args []string, verbs ...string) int {
	if r := spec.parse(args); r.operandAt >= 0 && slices.Contains(verbs, args[r.operandAt]) {
		return r.operandAt
	}
	valueOK := false
	for i, a := range args {
		switch {
		case a == "--":
			return -1
		case len(a) > 1 && a[0] == '-':
			valueOK = !strings.Contains(a, "=")
		case slices.Contains(verbs, a):
			return i
		case valueOK:
			valueOK = false
		default:
			return -1
		}
	}
	return -1
}

// commandAfter returns the command at the head of args once spec's options
// (and a `--` terminator) are skipped.
func commandAfter(spec optSpec, args []string) []string {
	r := spec.parse(args)
	switch {
	case r.operandAt >= 0:
		return args[r.operandAt:]
	case len(r.rest) > 0:
		return r.rest
	}
	return nil
}

// yarnSubcommands are the yarn verbs that are not a bin or script name; any
// other first operand makes yarn run that command.
var yarnSubcommands = fieldSet("add bin cache check config create dedupe dlx exec explain generate-lock-entry " +
	"global help import info init install licenses link list login logout node npm outdated owner pack patch " +
	"plugin policies publish rebuild remove run set search tag team test unlink unplug up upgrade " +
	"upgrade-interactive version versions why workspace workspaces constraints start build lint " +
	"audit access autoclean patch-commit stage")

// yarnRunVerbs are the yarn verbs that run a command or that read through
// to another yarn command.
var yarnRunVerbs = []string{"exec", "dlx", "node", "run", "workspace", "workspaces"}

// yarnRunner returns the command a yarn invocation runs and whether it runs
// it as a binary (`yarn exec|dlx|node CMD`). `yarn run WORD` and a bare
// `yarn WORD` name a script or a bin, which npm run and pnpm treat the same
// way, so they keep the verdict of the command they name without an extra
// floor. `yarn workspace NAME …` and `yarn workspaces foreach|run …` run the
// yarn command that follows in other workspaces and are read through.
func yarnRunner(tokens []string) ([]string, bool) {
	args := tokens[1:]
	for len(args) > 0 {
		r := yarnRunnerSpec.parse(args)
		if r.operandAt < 0 {
			return nil, false
		}
		at := r.operandAt
		if !slices.Contains(yarnRunVerbs, args[at]) {
			if loose := runnerVerbAt(yarnRunnerSpec, args, yarnRunVerbs...); loose >= 0 {
				at = loose
			}
		}
		verb, rest := args[at], args[at+1:]
		switch verb {
		case "exec", "dlx", "node":
			return rest, true
		case "run":
			if len(rest) > 0 && rest[0] == "--" {
				rest = rest[1:]
			}
			return rest, false
		case "workspace":
			if len(rest) == 0 {
				return nil, false
			}
			args = rest[1:] // the workspace name
			continue
		case "workspaces":
			if len(rest) == 0 {
				return nil, false
			}
			switch rest[0] {
			case "foreach":
				args = commandAfter(yarnForeachSpec, rest[1:])
				continue
			case "run":
				// Yarn 1: `yarn workspaces run SCRIPT`.
				return rest[1:], false
			}
			return nil, false
		}
		if yarnSubcommands[verb] {
			return nil, false
		}
		return args[at:], false
	}
	return nil, false
}

// yarnRunsBin reports whether a yarn invocation runs a binary explicitly
// (`yarn exec|dlx|node CMD`), also through `yarn workspace NAME` and
// `yarn workspaces foreach`.
func yarnRunsBin(tokens []string) bool {
	_, bin := yarnRunner(tokens)
	return bin
}

// bunxCommand returns the command after bunx's own options (--bun,
// -p/--package PKG).
func bunxCommand(args []string) []string {
	return commandAfter(optSpec{posix: true, short: "p", long: longTable("package", "")}, args)
}

// npmExecCommand returns the command `npm exec` / npx runs from its
// arguments, or nil when -c/--call carries a command line instead (see
// runnerCommandStrings).
func npmExecCommand(args []string) []string {
	for _, o := range npmExecSpec.parse(args).opts {
		if o.is("-c", "--call") {
			return nil
		}
	}
	return commandAfter(npmExecSpec, args)
}

// runnerCommand returns the command a package or version manager runs on
// behalf of its caller (`pnpm -C x exec CMD`, `yarn CMD`, `bun x CMD`,
// `npm --prefix x exec CMD`, `npx CMD`, `uvx CMD`, `tox exec -- CMD`,
// `nvm exec 18 CMD`, `fnm exec CMD`, `pyenv exec CMD`, `rbenv exec CMD`,
// `poetry -C x run CMD`), or nil. Script names (`yarn run build`) are
// returned as the command they name.
func runnerCommand(name string, tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	args := tokens[1:]
	after := func(spec optSpec, verbs ...string) []string {
		at := runnerVerbAt(spec, args, verbs...)
		if at < 0 {
			return nil
		}
		rest := args[at+1:]
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		return rest
	}
	switch name {
	case "pnpm":
		return after(pnpmRunnerSpec, "exec", "dlx", "x")
	case "bun":
		return bunxCommand(after(bunRunnerSpec, "x"))
	case "bunx":
		return bunxCommand(args)
	case "npx":
		return npmExecCommand(args)
	case "npm":
		at := runnerVerbAt(npmGlobalSpec, args, "exec", "x")
		if at < 0 {
			return nil
		}
		return npmExecCommand(args[at+1:])
	case "uvx":
		return commandAfter(uvToolRunSpec, args)
	case "uv":
		if rest := after(uvGlobalSpec, "tool"); len(rest) > 0 && (rest[0] == "run" || rest[0] == "x") {
			return commandAfter(uvToolRunSpec, rest[1:])
		}
		return commandAfter(uvRunSpec, after(uvGlobalSpec, "run"))
	case "poetry", "pipenv", "pdm", "rye", "hatch", "bundle", "bundler":
		// A global option the wrapper grammar does not know leaves these
		// wrapped; read the verb through it here.
		verbs := []string{"run"}
		if name == "bundle" || name == "bundler" {
			verbs = []string{"exec", "e"}
		}
		return commandAfter(projectRunnerSpec, after(projectRunnerGlobals[name], verbs...))
	case "tox":
		// tox exec -e ENV -- CMD: the command follows the terminator.
		at := runnerVerbAt(toxSpec, args, "exec", "e")
		if at < 0 {
			return nil
		}
		if i := slices.Index(args[at+1:], "--"); i >= 0 {
			return args[at+1+i+1:]
		}
		return nil
	case "pyenv", "rbenv":
		return after(optSpec{posix: true}, "exec")
	case "fnm":
		return commandAfter(fnmRunnerSpec, after(fnmRunnerSpec, "exec"))
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
		cmd, _ := yarnRunner(tokens)
		return cmd
	}
	return nil
}

// runnerCommandStrings returns the shell command lines a package runner
// runs: `npm exec -c CMD`, `npx -c CMD` and `npm explore PKG -- CMD`, whose
// words npm joins and hands to a shell.
func runnerCommandStrings(name string, tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	args := tokens[1:]
	switch name {
	case "npm":
		at := runnerVerbAt(npmGlobalSpec, args, "exec", "x", "explore")
		if at < 0 {
			return nil
		}
		if args[at] != "explore" {
			return npmCallStrings(args[at+1:])
		}
		r := (optSpec{posix: true}).parse(args[at+1:])
		var words []string
		switch {
		case r.operandAt >= 0:
			words = r.operands[1:] // after the package name
			if len(words) > 0 && words[0] == "--" {
				words = words[1:]
			}
		}
		if cmd := strings.Join(words, " "); strings.TrimSpace(cmd) != "" {
			return []string{cmd}
		}
	case "npx":
		return npmCallStrings(args)
	}
	return nil
}

// npmCallStrings returns the -c/--call values of an `npm exec` / npx
// argument list.
func npmCallStrings(args []string) []string {
	var out []string
	for _, o := range npmExecSpec.parse(args).opts {
		if o.has && o.is("-c", "--call") {
			out = append(out, o.value)
		}
	}
	return out
}

// npmAuthKey reports whether key is one of the npm and yarn configuration
// keys that hold a registry credential.
func npmAuthKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"_authtoken", "_auth", "_password", "npmauthtoken", "npmauthident", "npmpassword"} {
		if k == s || strings.HasSuffix(k, ":"+s) || strings.HasSuffix(k, "."+s) {
			return true
		}
	}
	return false
}

// packageConfigPrintsCredential reports whether a package manager command
// prints a registry credential (`npm config get //host/:_authToken`,
// `yarn config get npmAuthToken`), which is the same secret `cat ~/.npmrc`
// reads.
func packageConfigPrintsCredential(name string, tokens []string) bool {
	switch name {
	case "npm", "pnpm", "yarn":
	default:
		return false
	}
	ops := (optSpec{long: npmGlobalSpec.long, short: npmGlobalSpec.short}).parse(tokens[1:]).operands
	// npm and pnpm also accept `get` without `config`.
	if len(ops) > 0 && ops[0] == "config" {
		ops = ops[1:]
	} else if name == "yarn" {
		return false
	}
	if len(ops) == 0 || ops[0] != "get" {
		return false
	}
	for _, key := range ops[1:] {
		if npmAuthKey(key) {
			return true
		}
	}
	return false
}
