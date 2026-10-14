package danger

import (
	"path/filepath"
	"slices"
	"strings"
)

// Runner option grammars: the value-taking global options that may sit
// between the runner and its subcommand.
var (
	pnpmRunnerSpec = optSpec{posix: true, short: "CF", long: longTable("dir filter reporter loglevel workspace-concurrency", "")}
	// pnpmExecSpec is the grammar of `pnpm exec|dlx|x`: -p/--package names a
	// package to install; -c/--shell-mode runs the command through a shell.
	pnpmExecSpec = optSpec{posix: true, short: "pF", long: longTable("package resume-from filter reporter "+
		"workspace-concurrency report-summary-file", "")}
	// yarnExecSpec is the grammar of `yarn exec|dlx`: -p/--package names a
	// package to install.
	yarnExecSpec  = optSpec{posix: true, short: "p", long: longTable("package", "")}
	bunRunnerSpec = optSpec{posix: true, short: "rdlFc", long: longTable("cwd config elide-lines env-file preload "+
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
// A `--` ends the global options only: the word after it is still read as
// the subcommand (`npm -- exec CMD`).
func runnerVerbAt(spec optSpec, args []string, verbs ...string) int {
	r := spec.parse(args)
	if r.operandAt >= 0 && slices.Contains(verbs, args[r.operandAt]) {
		return r.operandAt
	}
	if r.operandAt < 0 && len(r.rest) > 0 && slices.Contains(verbs, r.rest[0]) {
		return len(args) - len(r.rest)
	}
	valueOK := false
	for i, a := range args {
		switch {
		case a == "--":
			if i+1 < len(args) && slices.Contains(verbs, args[i+1]) {
				return i + 1
			}
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
	y := yarnRun(tokens)
	return y.cmd, y.bin
}

// yarnReading is what a yarn invocation runs and where.
type yarnReading struct {
	cmd []string
	bin bool
	// dir is the directory --cwd selects; dirUnknown is set once
	// `yarn workspace NAME` or `yarn workspaces foreach|run` picks
	// workspaces the analysis cannot locate.
	dir        string
	dirUnknown bool
}

func yarnRun(tokens []string) yarnReading {
	var y yarnReading
	args := tokens[1:]
	for len(args) > 0 {
		r := yarnRunnerSpec.parse(args)
		for _, o := range r.opts {
			if o.is("--cwd") {
				y.dir = joinRunnerDir(y.dir, o.value, o.has)
			}
		}
		if r.operandAt < 0 {
			return y
		}
		at := r.operandAt
		if !slices.Contains(yarnRunVerbs, args[at]) {
			if loose := runnerVerbAt(yarnRunnerSpec, args, yarnRunVerbs...); loose >= 0 {
				at = loose
			}
		}
		verb, rest := args[at], args[at+1:]
		switch verb {
		case "exec", "dlx":
			y.cmd, y.bin = commandAfter(yarnExecSpec, rest), true
			return y
		case "node":
			y.cmd, y.bin = append([]string{"node"}, rest...), true
			return y
		case "run":
			if len(rest) > 0 && rest[0] == "--" {
				rest = rest[1:]
			}
			y.cmd = rest
			return y
		case "workspace":
			if len(rest) == 0 {
				return y
			}
			y.dirUnknown = true
			args = rest[1:] // the workspace name
			continue
		case "workspaces":
			if len(rest) == 0 {
				return y
			}
			y.dirUnknown = true
			switch rest[0] {
			case "foreach":
				args = commandAfter(yarnForeachSpec, rest[1:])
				continue
			case "run":
				// Yarn 1: `yarn workspaces run SCRIPT`.
				y.cmd = rest[1:]
			}
			return y
		}
		if !yarnSubcommands[verb] {
			y.cmd = args[at:]
		}
		return y
	}
	return y
}

// joinRunnerDir applies a directory option value to the directory selected
// so far. A missing value or one the analysis cannot resolve (variables,
// globs) gives "$", which runnerDirectory reports as unknown.
func joinRunnerDir(dir, value string, has bool) string {
	switch {
	case !has || strings.ContainsAny(value, "$`*?[") || strings.Contains(dir, "$"):
		return "$"
	case dir == "" || filepath.IsAbs(value):
		return value
	}
	return filepath.Join(dir, value)
}

// runnerDirectory reports the working directory a package runner's options
// give the command it runs. has is false when no option selects one. dir is
// the directory named by pnpm -C|--dir, yarn --cwd or bun --cwd. known is
// false when the directory cannot be located statically: workspaces chosen
// by name, filter or recursion (`yarn workspace NAME`, `yarn workspaces
// foreach`, `npm -w|--workspace|--workspaces`, `pnpm --filter|-r`), or a
// value with variables or globs.
func runnerDirectory(name string, tokens []string) (dir string, known, has bool) {
	if len(tokens) < 2 {
		return "", true, false
	}
	args := tokens[1:]
	unknown := false
	switch name {
	case "pnpm":
		scan := func(r optResult) {
			for _, o := range r.opts {
				switch {
				case o.is("-C", "--dir"):
					dir, has = joinRunnerDir(dir, o.value, o.has), true
				case o.is("-F", "--filter", "-r", "--recursive"):
					unknown, has = true, true
				}
			}
		}
		scan(pnpmRunnerSpec.parse(args))
		if at := runnerVerbAt(pnpmRunnerSpec, args, "exec", "dlx", "x"); at >= 0 {
			scan(pnpmExecSpec.parse(args[at+1:]))
		}
	case "yarn":
		y := yarnRun(tokens)
		dir, unknown = y.dir, y.dirUnknown
		has = dir != "" || unknown
	case "bun":
		for _, o := range bunRunnerSpec.parse(args).opts {
			if o.is("--cwd") {
				dir, has = joinRunnerDir(dir, o.value, o.has), true
			}
		}
	case "npm":
		scan := func(r optResult) {
			for _, o := range r.opts {
				if o.is("-w", "--workspace", "--workspaces", "--ws") {
					unknown, has = true, true
				}
			}
		}
		scan(npmGlobalSpec.parse(args))
		if at := runnerVerbAt(npmGlobalSpec, args, npmRunnerVerbs...); at >= 0 {
			scan(npmExecSpec.parse(args[at+1:]))
		}
	}
	if unknown || dir == "$" {
		return "", false, has
	}
	return dir, true, has
}

// runnerRunsRelativeScript reports whether cmd executes a script named by a
// relative path: a relative program path, a script file as the command, or
// the first operand of an interpreter (also a `-m` module).
func runnerRunsRelativeScript(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	relative := func(tok string) bool { return !filepath.IsAbs(expandShellTokenPath(tok)) }
	first := cmd[0]
	if strings.Contains(first, "/") || scriptFileExtensions[strings.ToLower(filepath.Ext(first))] {
		return relative(first)
	}
	name := commandName(first)
	if !isScriptInterpreter(name) && name != "source" && name != "." {
		return false
	}
	for _, tok := range cmd[1:] {
		switch {
		case inlinePayloadFlag(name, tok):
			return false
		case tok == "-m":
			return true // a module resolved from the working directory
		case strings.HasPrefix(tok, "-"):
			continue
		}
		return relative(tok)
	}
	return false
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

// npm resolves any unambiguous prefix of a command name, and `x` is an alias
// of exec. npmExecVerbs and npmExploreVerbs list the spellings of exec and
// explore; npmAmbiguousVerbs are the shorter prefixes shared by exec,
// explain and explore, read as either verb so a spelling npm might resolve
// never hides the command.
var (
	npmExecVerbs      = []string{"exec", "exe", "x"}
	npmExploreVerbs   = []string{"explore", "explor", "explo"}
	npmAmbiguousVerbs = []string{"ex", "exp", "expl"}
	npmRunnerVerbs    = slices.Concat(npmExecVerbs, npmExploreVerbs, npmAmbiguousVerbs)
)

// pnpmShellMode reports whether `pnpm exec|dlx` options select shell mode
// (-c/--shell-mode), which runs the command words as a shell command line.
func pnpmShellMode(args []string) bool {
	for _, o := range pnpmExecSpec.parse(args).opts {
		if o.is("-c", "--shell-mode") {
			return true
		}
	}
	return false
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
		rest := after(pnpmRunnerSpec, "exec", "dlx", "x")
		if pnpmShellMode(rest) {
			return nil // a command line: see runnerCommandStrings
		}
		return commandAfter(pnpmExecSpec, rest)
	case "bun":
		return bunxCommand(after(bunRunnerSpec, "x"))
	case "bunx":
		return bunxCommand(args)
	case "npx":
		return npmExecCommand(args)
	case "npm":
		at := runnerVerbAt(npmGlobalSpec, args, npmRunnerVerbs...)
		if at < 0 || slices.Contains(npmExploreVerbs, args[at]) {
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
		return commandAfter(projectRunSpec(name), after(projectRunnerGlobals[name], verbs...))
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
// runs: `npm exec -c CMD`, `npx -c CMD`, `npm explore PKG -- CMD` (whose
// words npm joins and hands to a shell) and `pnpm exec|dlx -c CMD`.
func runnerCommandStrings(name string, tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	args := tokens[1:]
	switch name {
	case "npm":
		at := runnerVerbAt(npmGlobalSpec, args, npmRunnerVerbs...)
		if at < 0 {
			return nil
		}
		var out []string
		if !slices.Contains(npmExploreVerbs, args[at]) {
			out = npmCallStrings(args[at+1:])
		}
		if !slices.Contains(npmExecVerbs, args[at]) {
			r := (optSpec{posix: true}).parse(args[at+1:])
			var words []string
			if r.operandAt >= 0 {
				words = r.operands[1:] // after the package name
				if len(words) > 0 && words[0] == "--" {
					words = words[1:]
				}
			}
			if cmd := strings.Join(words, " "); strings.TrimSpace(cmd) != "" {
				out = append(out, cmd)
			}
		}
		return out
	case "npx":
		return npmCallStrings(args)
	case "pnpm":
		at := runnerVerbAt(pnpmRunnerSpec, args, "exec", "dlx", "x")
		if at < 0 || !pnpmShellMode(args[at+1:]) {
			return nil
		}
		if cmd := strings.Join(commandAfter(pnpmExecSpec, args[at+1:]), " "); strings.TrimSpace(cmd) != "" {
			return []string{cmd}
		}
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
