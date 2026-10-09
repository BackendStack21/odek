package danger

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var projectCodeTools = map[string]bool{
	"cmake": true, "ninja": true, "meson": true, "mvn": true, "mvnw": true, "gradle": true, "gradlew": true,
	"javac": true, "rustc": true, "tsc": true, "eslint": true, "prettier": true, "mypy": true, "flake8": true,
	"rubocop": true, "dotnet": true, "sbt": true, "swift": true, "swiftc": true, "kotlinc": true,
	"golangci-lint": true, "gdb": true, "lldb": true,
}

func adapterRunsCode(name string, tokens []string, repo *gitRepoCtx) bool {
	if hasAny([]string{"awk", "gawk", "mawk", "nawk"}, name) && optionPresent(tokens, "-l", "--load") {
		return true
	}
	if projectCodeTools[name] {
		return len(tokens) == 1 || !networkInfoQuery(tokens)
	}
	if name == "cargo" {
		return hasAny(tokens, "build", "test", "check", "clippy", "run", "bench", "rustc", "doc")
	}
	if name == "go" {
		return hasAny(tokens, "build", "test", "vet", "run", "generate", "tool", "install")
	}
	if name == "rg" {
		return optionPresent(tokens, "--pre", "--hostname-bin")
	}
	if name == "sort" {
		return longOptionAbbreviated(tokens, "compress-program")
	}
	if name == "sdiff" {
		return longOptionAbbreviated(tokens, "diff-program")
	}
	if name == "ssh" || name == "scp" || name == "sftp" || name == "rsync" {
		return transferClientRunsProgram(name, tokens)
	}
	if name == "fd" || name == "fdfind" {
		return optionPresent(tokens, "--exec", "--exec-batch", "-x", "-X")
	}
	if name == "gcc" || name == "g++" || name == "cc" || name == "c++" || name == "clang" || name == "clang++" {
		return optionPresent(tokens, "-fplugin", "-specs", "--specs", "-wrapper", "-B", "-Xclang", "-load")
	}
	if name == "protoc" {
		return !networkInfoQuery(tokens)
	}
	if name == "git" {
		sub, args := gitSubcommandAndArgs(tokens)
		switch sub {
		case "difftool", "mergetool":
			// These launch the configured diff/merge tool by design.
			return true
		case "commit", "merge", "checkout", "switch", "cherry-pick", "am", "add", "status", "restore", "stash", "gc",
			"revert", "reset", "clean", "rm", "mv", "update-index", "diff-files", "diff-index", "ls-files", "grep",
			"blame", "describe", "checkout-index", "pull", "fetch", "push":
			return gitVerbRunsRepoCode(sub, args, tokens, repo)
		case "worktree", "submodule":
			return !hasAny(args, "list", "status") && gitVerbRunsRepoCode(sub, args, tokens, repo)
		case "rebase":
			return !hasAny(args, "--abort", "--quit") && gitVerbRunsRepoCode(sub, args, tokens, repo)
		case "bisect", "hook":
			// `bisect run <cmd>` executes <cmd> per step; `hook run` runs the
			// repository hook script.
			return len(args) > 0 && args[0] == "run"
		case "diff", "show", "log":
			if gitExplicitDiffProgram(tokens) {
				return true
			}
			return gitVerbRunsRepoCode(sub, args, tokens, repo)
		}
	}
	if name == "curl" && optionPresent(tokens, "-K", "--config") {
		return true
	}
	if name == "wget" && optionPresent(tokens, "--config", "-i", "--input-file") {
		return true
	}
	return false
}

// gitWorktreeWriteTargets returns the destination path operands of
// `git worktree add` (the path) and `git worktree move` (the destination).
func gitWorktreeWriteTargets(args []string) []string {
	if len(args) == 0 || (args[0] != "add" && args[0] != "move") {
		return nil
	}
	var operands []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case a == "-b" || a == "-B" || a == "--reason":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			operands = append(operands, a)
		}
	}
	if args[0] == "add" {
		if len(operands) == 0 {
			return []string{dynamicSubstToken}
		}
		return operands[:1]
	}
	if len(operands) < 2 {
		return []string{dynamicSubstToken}
	}
	return operands[1:2]
}

// gitExplicitDiffProgram reports whether a diff-producing invocation asks for
// an external diff driver or textconv filter on the command line (including
// unambiguous abbreviations of --ext-diff and --textconv), which runs a
// configured program whatever the repository state.
func gitExplicitDiffProgram(tokens []string) bool {
	for _, tok := range tokens[1:] {
		if tok == "--" {
			break
		}
		if gitLongOpt(tok, "ext-diff", 3) || gitLongOpt(tok, "textconv", 3) {
			return true
		}
	}
	return false
}

// longOptionAbbreviated reports whether any token is the GNU long option full
// (or an unambiguous-prefix abbreviation of it, which getopt_long accepts),
// with or without an =value. An ambiguous prefix is a tool error, so flagging
// it costs nothing.
func longOptionAbbreviated(tokens []string, full string) bool {
	for _, tok := range tokens[1:] {
		if tok == "--" {
			break
		}
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, _, _ := strings.Cut(tok[2:], "=")
		if name != "" && strings.HasPrefix(full, name) {
			return true
		}
	}
	return false
}

// sshConfigOptionRunsProgram reports whether an ssh_config keyword given via
// -o (as "Key=value" or "Key value") makes the client run a program or load a
// library.
func sshConfigOptionRunsProgram(opt string) bool {
	key := strings.ToLower(strings.TrimLeft(opt, " \t"))
	if end := strings.IndexAny(key, "= \t"); end >= 0 {
		key = key[:end]
	}
	switch key {
	case "proxycommand", "localcommand", "knownhostscommand", "pkcs11provider",
		"securitykeyprovider", "xauthlocation", "include":
		return true
	}
	return false
}

// transferClientRunsProgram reports whether an ssh / scp / sftp / rsync
// invocation makes the client execute a local program or an attacker-chosen
// config file: -F config, -o ProxyCommand/LocalCommand/..., scp/sftp -S and -D
// program, rsync -e / --rsh remote-shell command. Short options are scanned
// through bundled clusters, where the first value-taking letter takes the rest
// of the word (or the next word) as its value.
func transferClientRunsProgram(name string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	spec := map[string]optSpec{"ssh": sshSyntax, "scp": scpSyntax, "sftp": sftpSyntax, "rsync": rsyncSyntax}[name]
	for _, o := range spec.parse(tokens[1:]).opts {
		switch {
		case name == "rsync":
			// -e is --rsh; a missing value leaves no program named, which is
			// read as one that cannot be vouched for. An ambiguous prefix is
			// an rsync error and runs nothing.
			if o.unique() && o.is("--rsh") && rsyncTransportRunsProgram(o.value) {
				return true
			}
		case o.is("-F"):
			return true
		case o.is("-o") && sshConfigOptionRunsProgram(o.value):
			return true
		case (name == "scp" || name == "sftp") && o.is("-S", "-D"):
			return true
		}
	}
	return false
}

// rsyncTransportRunsProgram reports whether an rsync -e/--rsh value runs
// something other than a plain ssh transport. `-e ssh` and `-e 'ssh -p 2222'`
// are the everyday remote-shell spelling and stay network egress; a path, a
// different program, or an ssh invocation that itself loads a program
// (ProxyCommand, -F, …) is local code execution.
func rsyncTransportRunsProgram(val string) bool {
	words := tokenize(val)
	if len(words) == 0 {
		return true
	}
	if words[0] != "ssh" {
		return true
	}
	return transferClientRunsProgram("ssh", words)
}

func optionPresent(tokens []string, options ...string) bool {
	for _, tok := range tokens[1:] {
		for _, option := range options {
			if tok == option || strings.HasPrefix(tok, option+"=") || (len(option) == 2 && strings.HasPrefix(tok, option) && len(tok) > 2 && tok[1] != '-') {
				return true
			}
		}
	}
	return false
}

func syntaxCheckArgumentsOnly(tokens []string, flags ...string) bool {
	operands := 0
	for _, tok := range tokens[1:] {
		if hasAny(flags, tok) || interpreterInfoFlags[tok] || tok == "--" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			return false
		}
		operands++
	}
	return operands == 1
}

func hostInspectorMutates(name string, tokens []string) bool {
	switch name {
	case "sysctl":
		for _, tok := range tokens[1:] {
			if strings.Contains(tok, "=") {
				return true
			}
		}
	case "hostname":
		for _, tok := range tokens[1:] {
			if !strings.HasPrefix(tok, "-") {
				return true
			}
		}
	case "ip":
		return hasAny(tokens, "add", "delete", "del", "set", "replace", "change", "flush", "restore", "exec")
	case "ifconfig":
		return len(tokens) > 2 && !networkInfoQuery(tokens)
	}
	return false
}

func formattingMutates(name string, tokens []string) bool {
	switch name {
	case "gofmt", "goimports", "gofumpt", "shfmt":
		return optionPresent(tokens, "-w", "--write")
	case "rustfmt", "black", "isort", "autopep8", "yapf", "stylua":
		return !networkInfoQuery(tokens) && !hasAny(tokens, "--check", "--diff")
	case "ruff":
		return hasAny(tokens, "--fix", "--fix-only") || (hasAny(tokens, "format") && !hasAny(tokens, "--check", "--diff"))
	case "go":
		return hasAny(tokens, "fmt") || (hasAny(tokens, "mod") && hasAny(tokens, "tidy", "edit"))
	case "kill", "pkill", "killall", "bg", "fg", "ulimit":
		return !networkInfoQuery(tokens) && len(tokens) > 1
	case "gcc", "g++", "cc", "c++", "clang", "clang++":
		return !networkInfoQuery(tokens) && !hasAny(tokens, "-E", "-fsyntax-only")
	case "docker", "docker-compose", "podman", "nerdctl":
		_, verb := containerVerb(containerVerbPath(name, tokens))
		return hasAny([]string{"down", "stop", "rm", "kill", "pause", "unpause", "restart", "rename", "update", "start"}, verb)
	}
	return false
}

func commandOnlyReads(name string, tokens []string) bool {
	switch name {
	case "sed":
		return !sedInPlace(tokens) && !sedRunsShellCode(tokens) && !sedHasFileIO(tokens)
	case "tar":
		if tarListsOnly(tokens) {
			return !tarRunsCommand(tokens)
		}
	case "unzip":
		return hasAny(tokens, "-l", "-v", "-Z")
	case "7z", "7za", "7zz":
		return len(tokens) > 1 && tokens[1] == "l"
	}
	return false
}

// tarListsOnly reports whether a tar invocation only lists an archive: a
// `--list` long option, or a short cluster whose mode letters (the letters
// before the first value-taking one) include `t` and no creating, extracting
// or modifying mode. Letters after a value-taking letter are that option's
// attached value (`-C/etc`, `-cftest.tar`), not further flags.
func tarListsOnly(tokens []string) bool {
	args := tokens[1:]
	list := false
	for _, o := range tarOptions.parse(args).opts {
		switch {
		case o.is("-t"):
			list = true
		case o.is("-c", "-x", "-r", "-u", "-A", "-d"):
			return false
		case o.is("--list") && args[o.at] == "--list":
			list = true
		}
	}
	return list
}

// sedOptions is the GNU sed option grammar: -e, -f and -l take a value, -i
// takes only a fused suffix, and long options may be abbreviated (`--in` is
// `--in-place`). `--` is not an end of options here, so nothing after it hides
// from the predicates built on it.
var sedOptions = optSpec{
	short:         "efl",
	shortOptional: "i",
	long: longTable("expression file line-length", "in-place binary debug follow-symlinks help null-data posix quiet "+
		"regexp-extended sandbox separate silent unbuffered version zero-terminated"),
	abbrev:         true,
	ignoreDashDash: true,
}

func sedInPlace(tokens []string) bool {
	return sedOptions.parse(tokens[1:]).has("-i", "--in-place")
}

// The filename may follow the command letter without a space when it is
// path-shaped (`w/tmp/x`, `w./out`), which GNU sed treats like `w /tmp/x`;
// a plain word starting with r or w (README.md) is not a file command.
var sedFileIOPattern = regexp.MustCompile(`(?:^|[;{}\n])\s*(?:[0-9$]+(?:,[0-9$]+)?\s*|/[^/]+/\s*)?([rwRW])(?:\s+([^;}\n]+)|([/.~][^;}\n]*))`)
var sedSubstitutionPrefix = regexp.MustCompile(`^(?:[0-9$]+(?:,[0-9$]+)?\s*|/[^/]+/\s*)?s`)

// Walk escaped delimiters rather than using the last slash: an s///w
// destination may itself contain slashes, and grouped scripts may continue.
func sedSubstitutionFlags(script string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(script, func(r rune) bool { return r == '{' || r == '}' || r == ';' || r == '\n' }) {
		part = strings.TrimSpace(part)
		prefix := sedSubstitutionPrefix.FindString(part)
		if prefix == "" || len(part) <= len(prefix) {
			continue
		}
		delim := part[len(prefix)]
		separators := 0
		for i := len(prefix) + 1; i < len(part); i++ {
			if part[i] == '\\' {
				i++
				continue
			}
			if part[i] == delim {
				separators++
				if separators == 2 {
					out = append(out, part[i+1:])
					break
				}
			}
		}
	}
	return out
}

func sedHasFileIO(tokens []string) bool {
	for _, tok := range sedScriptTexts(tokens) {
		if sedFileIOPattern.MatchString(tok) {
			return true
		}
		for _, flags := range sedSubstitutionFlags(tok) {
			if strings.Contains(flags, "w") {
				return true
			}
		}
	}
	return false
}

// sedScriptTexts returns every word of a sed command line that may hold script
// text. Any word can (the script is usually the first operand, and the
// predicates are cautious about the rest), so it returns them all; a script
// fused into the option that introduces it (`-es/a/b/w`, `--expression=p`)
// is returned without the option.
func sedScriptTexts(tokens []string) []string {
	words := append([]string(nil), tokens[1:]...)
	for _, o := range sedOptions.parse(tokens[1:]).opts {
		if o.has && o.end == o.at && o.is("-e", "--expression") {
			words[o.at] = o.value
		}
	}
	return words
}

func executionFileTargets(name string, tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	var options []string
	// commandOptions take a debugger/editor command string whose `source`
	// style commands load a script file.
	var commandOptions []string
	switch name {
	case "awk", "gawk", "mawk", "nawk":
		// -f/--file name the awk program; -i/--include and -l/--load pull
		// in more awk source or extension libraries.
		options = []string{"-l", "--load", "-f", "--file", "-i", "--include"}
	case "sed":
		options = []string{"-f", "--file"}
	case "emacs":
		options = []string{"--script", "-l", "--load", "-x"}
	case "vi", "vim", "view", "ex", "rvim", "gvim", "nvim":
		// -S sources a session script, -u a vimrc, -s a keystroke script; nvim
		// -l runs a Lua file. -c/--cmd and +cmd run ex commands.
		options = []string{"-S", "-u", "-U", "-s"}
		if name == "nvim" {
			options = append(options, "-l")
		}
		commandOptions = []string{"-c", "--cmd"}
	case "make", "gmake":
		options = []string{"-f", "--file", "--makefile"}
	case "just":
		options = []string{"-f", "--justfile"}
	case "gdb":
		options = []string{"-x", "--command", "-ix", "--init-command"}
		commandOptions = []string{"-ex", "--eval-command", "-iex", "--init-eval-command"}
	case "lldb":
		options = []string{"-s", "--source", "-S", "--source-before-file", "-k", "--source-on-crash", "-K", "--source-on-crash-before-file"}
		commandOptions = []string{"-o", "--one-line", "-O", "--one-line-before-file"}
	case "rg":
		options = []string{"--pre"}
	case "tar":
		options = []string{"-I", "--use-compress-program", "--to-command", "--checkpoint-action"}
	case "node":
		options = []string{"--require", "-r", "--import", "--loader", "--experimental-loader"}
	case "gcc", "cc", "g++", "c++", "clang", "clang++":
		options = []string{"-fplugin", "-specs", "--specs", "-wrapper", "-load"}
	case "go":
		options = []string{"-toolexec"}
	case "protoc":
		options = []string{"--plugin"}
	case "sqlite3":
		var out []string
		for _, tok := range tokens[1:] {
			parts := strings.Fields(tok)
			if len(parts) > 1 && hasAny([]string{".read", ".load"}, parts[0]) {
				out = append(out, parts[1])
			}
		}
		return out
	}
	all := append(append([]string(nil), commandOptions...), options...)
	spec := valueOptionSpec(all, executionValueLetters[name])
	args := tokens[1:]
	var out []string
	for i := 0; i < len(args); {
		tok := args[i]
		if len(tok) > 1 && tok[0] == '+' && hasAny(commandOptionOwners, name) {
			out = append(out, sourceCommandFiles(tok[1:])...)
			i++
			continue
		}
		if len(tok) < 2 || tok[0] != '-' {
			i++
			continue
		}
		opts, next := spec.option(args, i)
		i = next
	options:
		for _, o := range opts {
			if !o.has || !o.unique() {
				continue
			}
			for _, option := range all {
				if !o.is(option) {
					continue
				}
				if o.value == "" {
					break options
				}
				if hasAny(commandOptions, option) {
					out = append(out, sourceCommandFiles(o.value)...)
					break options
				}
				value := o.value
				if name == "tar" {
					value = strings.TrimPrefix(value, "exec=")
				}
				if name == "protoc" {
					if _, path, ok := strings.Cut(value, "="); ok {
						value = path
					}
				}
				if words := tokenize(value); len(words) > 0 {
					out = append(out, words[0])
				}
				break options
			}
		}
	}
	return out
}

// executionValueLetters are the value-taking short letters of a tool beyond
// the program-loading options executionFileTargets looks for, so a cluster such
// as `awk -vf=1` is not read as `-f`.
var executionValueLetters = map[string]string{
	"awk": "vFeEW", "gawk": "vFeEW", "mawk": "vFeEW", "nawk": "vFeEW",
	"sed":  "el",
	"make": "CIoW", "gmake": "CIoW",
}

// valueOptionSpec builds the grammar of a tool from the options that take a
// value and that a caller looks for: two-character options are short letters,
// `--name` options are long ones, and longer single-dash options are exact
// words. extraShort adds value-taking letters the caller does not look for.
// Long options may be abbreviated to two characters, and `--` ends nothing, so
// the scan sees every word.
func valueOptionSpec(options []string, extraShort string) optSpec {
	spec := optSpec{short: extraShort, long: make(map[string]bool), abbrev: true, minAbbrev: 2, ignoreDashDash: true}
	for _, o := range options {
		switch {
		case strings.HasPrefix(o, "--"):
			spec.long[o[2:]] = true
		case len(o) == 2:
			spec.short += o[1:]
		default:
			spec.exact = append(spec.exact, o)
		}
	}
	return spec
}

// commandOptionOwners are the editors whose +CMD arguments run ex commands.
var commandOptionOwners = []string{"vi", "vim", "view", "ex", "rvim", "gvim", "nvim"}

// semanticWriteTargets extracts destinations that are not shell redirects.
// Uninspectable destination construction uses the dynamic marker and fails closed.
func semanticWriteTargets(name string, tokens []string) []string {
	var targets []string
	if len(tokens) == 0 {
		return targets
	}
	if networkInfoQuery(tokens) {
		return targets
	}
	switch name {
	case "dd":
		for _, tok := range tokens[1:] {
			if value, ok := strings.CutPrefix(tok, "of="); ok {
				if value == "" {
					value = dynamicSubstToken
				}
				targets = append(targets, value)
			}
		}
	case "gofmt", "goimports", "gofumpt", "shfmt":
		if formattingMutates(name, tokens) {
			for _, tok := range tokens[1:] {
				if !strings.HasPrefix(tok, "-") {
					targets = append(targets, tok)
				}
			}
		}
	case "gh":
		targets = append(targets, ghWriteTargets(tokens)...)
	case "git":
		// --output=FILE on the history/diff viewers and archive writes FILE;
		// archive also takes the short -o FILE / -oFILE spelling.
		switch sub, args := gitSubcommandAndArgs(tokens); sub {
		case "worktree":
			// `worktree add PATH` creates a directory tree at PATH and
			// `worktree move SRC DST` relocates one; PATH is a write target.
			targets = append(targets, gitWorktreeWriteTargets(args)...)
		case "archive":
			targets = append(targets, writeOption{spec: gitArchiveOptions, names: []string{"-o", "--output"}}.targets(args)...)
		case "log", "show", "diff", "whatchanged", "format-patch", "range-diff", "shortlog":
			targets = append(targets, writeOption{spec: gitOutputOptions, names: []string{"--output"}, endsAtDashDash: true}.targets(args)...)
		}
	case "sed":
		for _, tok := range sedScriptTexts(tokens) {
			for _, match := range sedFileIOPattern.FindAllStringSubmatch(tok, -1) {
				if strings.EqualFold(match[1], "w") {
					targets = append(targets, strings.TrimSpace(match[2]+match[3]))
				} else {
					targets = append(targets, dynamicSubstToken)
				}
			}
			for _, suffix := range sedSubstitutionFlags(tok) {
				if _, path, ok := strings.Cut(suffix, "w"); ok {
					targets = append(targets, strings.TrimSpace(path))
				}
			}
		}
		if sedInPlace(tokens) {
			for _, tok := range tokens[2:] {
				if !strings.HasPrefix(tok, "-") && !strings.ContainsAny(tok, "; {}") {
					targets = append(targets, tok)
				}
			}
		}
	case "awk", "gawk", "mawk", "nawk":
		for _, tok := range tokens[1:] {
			if strings.ContainsAny(tok, "<>") || strings.Contains(tok, "getline") {
				targets = append(targets, dynamicSubstToken)
			}
		}
	case "sqlite3":
		for _, tok := range tokens[1:] {
			parts := strings.Fields(tok)
			if len(parts) > 1 && hasAny([]string{".output", ".once", ".save", ".backup"}, parts[0]) {
				targets = append(targets, parts[1])
			}
		}
	case "xxd":
		if hasAny(tokens, "-r", "-revert") {
			var operands []string
			for _, tok := range tokens[1:] {
				if !strings.HasPrefix(tok, "-") {
					operands = append(operands, tok)
				}
			}
			if len(operands) > 1 {
				targets = append(targets, operands[len(operands)-1])
			}
		}
	}
	if wo, ok := writeOptions[name]; ok {
		targets = append(targets, wo.targets(tokens[1:])...)
	}
	if name == "curl" || name == "wget" {
		r := writeOptions[name].spec.parse(tokens[1:])
		dir := "."
		dirOptions := []string{"--output-dir"}
		if name == "wget" {
			dirOptions = []string{"-P", "--directory-prefix"}
		}
		for _, o := range r.opts {
			if o.has && o.is(dirOptions...) {
				dir = o.value
			}
		}
		for i, target := range targets {
			if target != "-" && dir != "." && !filepath.IsAbs(target) {
				targets[i] = filepath.Join(dir, target)
			}
		}
		// Without an explicit name the file is named after the URL.
		if (name == "wget" && !r.has("--output-document")) || (name == "curl" && r.has("-O", "--remote-name", "--remote-name-all")) {
			found := false
			for _, tok := range tokens[1:] {
				u, err := url.Parse(tok)
				if err == nil && u.Scheme != "" && u.Host != "" {
					base := filepath.Base(u.Path)
					if base == "." || base == "/" {
						base = "index.html"
					}
					targets = append(targets, filepath.Join(dir, base))
					found = true
				}
			}
			if !found {
				targets = append(targets, dynamicSubstToken)
			}
		}
	}
	return targets
}

// writeOption describes where a tool puts files it is told about: its option
// grammar and the canonical names of the options whose value is a path the
// tool writes.
type writeOption struct {
	spec  optSpec
	names []string
	// endsAtDashDash stops the scan at `--`. By default the scan reads every
	// word, so an option cannot hide behind a terminator.
	endsAtDashDash bool
}

// targets returns the paths the named options of args write. An option that
// should carry a path but has none (the arguments end first) writes somewhere
// unknown.
func (w writeOption) targets(args []string) []string {
	spec := w.spec
	spec.ignoreDashDash = !w.endsAtDashDash
	var out []string
	for _, o := range spec.parse(args).opts {
		switch {
		case !o.is(w.names...):
		case o.has:
			out = append(out, o.value)
		case o.takes:
			out = append(out, dynamicSubstToken)
		}
	}
	return out
}

// writeOptions are the tools whose options name an output file or directory.
var writeOptions = map[string]writeOption{
	"curl":    {spec: curlSyntax, names: []string{"-o", "--output", "-D", "--dump-header", "-c", "--cookie-jar", "--trace", "--trace-ascii"}},
	"wget":    {spec: wgetSyntax, names: []string{"--output-document", "-o", "--output-file", "-a", "--append-output"}},
	"sort":    {spec: sortOptions, names: []string{"-o", "--output"}},
	"gcc":     {spec: gccOptions, names: []string{"-o"}},
	"g++":     {spec: gccOptions, names: []string{"-o"}},
	"cc":      {spec: gccOptions, names: []string{"-o"}},
	"c++":     {spec: gccOptions, names: []string{"-o"}},
	"clang":   {spec: gccOptions, names: []string{"-o"}},
	"clang++": {spec: gccOptions, names: []string{"-o"}},
	"gpg":     {spec: gpgOptions, names: []string{"-o", "--output"}},
	"gpg2":    {spec: gpgOptions, names: []string{"-o", "--output"}},
	"find":    {spec: findOptions, names: []string{"-fprint", "-fprint0", "-fprintf"}},
	"cp":      {spec: cpOptions, names: []string{"-t", "--target-directory"}},
	"mv":      {spec: cpOptions, names: []string{"-t", "--target-directory"}},
	"install": {spec: modeOptions["install"], names: []string{"-t", "--target-directory"}},
	"tar":     {spec: tarOptions, names: []string{"-C", "--directory"}},
	"unzip":   {spec: optSpec{short: "d"}, names: []string{"-d"}},
	"7z":      {spec: optSpec{short: "o"}, names: []string{"-o"}},
	"7za":     {spec: optSpec{short: "o"}, names: []string{"-o"}},
	"7zz":     {spec: optSpec{short: "o"}, names: []string{"-o"}},
	"pandoc":  {spec: optSpec{short: "o", long: valueOpts("output"), abbrev: true}, names: []string{"-o", "--output"}},
}

var (
	sortOptions = optSpec{
		short: "ktTSo",
		long: valueOpts("output key field-separator temporary-directory buffer-size parallel compress-program " +
			"files0-from random-source batch-size sort"),
		abbrev: true,
	}
	gccOptions  = optSpec{short: "o"}
	gpgOptions  = optSpec{short: "rpuo", long: valueOpts("output recipient local-user"), abbrev: true}
	findOptions = optSpec{exact: []string{"-fprint", "-fprint0", "-fprintf"}}
	cpOptions   = optSpec{short: "tS", long: valueOpts("target-directory suffix"), abbrev: true}

	// git archive writes the file named by -o/--output; the history and diff
	// viewers write --output only, and git reads their abbreviation from four
	// characters on.
	gitArchiveOptions = optSpec{short: "o", long: valueOpts("output"), abbrev: true}
	gitOutputOptions  = optSpec{long: valueOpts("output"), abbrev: true, minAbbrev: 4}
)
