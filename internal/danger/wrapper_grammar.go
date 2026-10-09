// Option grammar for the wrappers that run another command. One table
// (wrapperSpecs) and one parser serve the unwrapping in the classifier and the
// denylist matcher, so both agree on which word is the wrapped command, which
// options consume a value, and which option carries a shell command line
// (script -c, flock -c, env -S, watch) that is analysed as a command rather
// than skipped. Subcommand wrappers (nix, mise, direnv exec, asdf exec) are
// handled by subcommandWrapper.

package danger

import "strings"

// wrapperSpecs are the option grammars of the wrappers that run another
// command: which options consume a value (so the value is not mistaken for
// the wrapped command). They accept unambiguous long-option prefixes as
// getopt_long does; when several options share a prefix, the value-taking
// reading wins so its value is not read as the wrapped command.
var wrapperSpecs = map[string]optSpec{
	"timeout": wrapperGrammar("sk", nil, "signal kill-after", "foreground preserve-status verbose"),
	"stdbuf":  wrapperGrammar("ioe", nil, "input output error", ""),
	"nice":    wrapperGrammar("n", nil, "adjustment", ""),
	"ionice":  wrapperGrammar("cnpPu", nil, "class classdata pid pgid uid", "ignore"),
	"chrt": wrapperGrammar("TPD", nil, "sched-runtime sched-period sched-deadline",
		"pid batch deadline fifo idle other rr reset-on-fork max all-tasks verbose"),
	"taskset": wrapperGrammar("", nil, "", "cpu-list pid all-tasks"),
	"flock": withAlias(wrapperGrammar("wEc", nil, "timeout wait conflict-exit-code command",
		"nonblock nb shared exclusive unlock close no-fork verbose"), 'c', "--command"),
	"script": withAlias(wrapperGrammar("cEIOBTmo", nil, "command echo log-in log-out log-io log-timing logging-format output-limit",
		"append flush force quiet return"), 'c', "--command"),
	"arch":   wrapperGrammar("", []string{"-arch", "-e", "-d"}, "", ""),
	"watch":  wrapperGrammar("n", nil, "interval equexit", "differences precise no-title beep errexit chgexit color exec no-linewrap"),
	"strace": wrapperGrammar("aAbeEIoOpPsSuX", nil, "output attach trace", ""),
	"sudo": wrapperGrammar("CDghprTtUu", nil, "user group chdir host prompt role type command-timeout other-user chroot close-from",
		"preserve-env login shell stdin non-interactive background askpass edit help list validate version "+
			"remove-timestamp reset-timestamp set-home bell preserve-groups"),
	"doas": wrapperGrammar("uC", nil, "", ""),
	// xargs and GNU parallel share one grammar. -i, -e and -l take an
	// optional value, which is only ever fused into the word: a bare `-e` or
	// `--replace` never takes the wrapped command as its value.
	"xargs":    xargsGrammar,
	"parallel": xargsGrammar,
	"xe":       xargsGrammar,
	"sem":      semGrammar,
	// env: -u NAME, -C DIR, -S STRING, -a NAME, -P PATH and their long
	// spellings.
	"env": withAlias(withAlias(wrapperGrammar("uCSaP", nil, "unset chdir split-string argv0", ""),
		'S', "--split-string"), 'u', "--unset"),
}

// semGrammar is the xargs grammar plus sem's own --id NAME option and its
// semaphore flags.
var semGrammar = optSpec{
	short:         xargsGrammar.short,
	shortOptional: xargsGrammar.shortOptional,
	long: longTable("max-lines max-args max-procs max-chars delimiter arg-file jobs id",
		"replace eof null exit interactive open-tty no-run-if-empty process-slot-var show-limits verbose fg wait semaphore"),
	abbrev: true,
}

var xargsGrammar = optSpec{
	short:         "ILnPsEdajN",
	shortOptional: "iel",
	long: longTable("max-lines max-args max-procs max-chars delimiter arg-file jobs",
		"replace eof null exit interactive open-tty no-run-if-empty process-slot-var show-limits verbose"),
	abbrev: true,
}

// wrapperOperands is the count of positional operands (priority, CPU list,
// lock file, typescript file) that precede the wrapped command.
var wrapperOperands = map[string]int{"chrt": 1, "taskset": 1, "flock": 1, "script": 1}

// wrapperGrammar builds the spec of a wrapper from its value-taking short
// letters, its single-dash value options, and its long options with and
// without a value.
func wrapperGrammar(short string, exact []string, longValue, longFlags string) optSpec {
	return optSpec{short: short, exact: exact, long: longTable(longValue, longFlags), abbrev: true}
}

// withAlias names a short option letter by a long option, so one canonical
// spelling matches both forms.
func withAlias(s optSpec, letter byte, long string) optSpec {
	alias := map[byte]string{letter: long}
	for k, v := range s.alias {
		alias[k] = v
	}
	s.alias = alias
	return s
}

// wrapperCarriesCommand reports whether the wrapper's `-c`/`--command` option
// carries a command string that the wrapper hands to a shell.
func wrapperCarriesCommand(name string) bool { return name == "flock" || name == "script" }

// wrapperStep is the result of reading one wrapper at the head of a command
// chain: where the wrapped command starts, the risk the wrapper itself
// imposes, and the shell command strings or assignments it carries.
type wrapperStep struct {
	name  string
	next  int
	floor RiskClass
	// payload is a command string the wrapper runs through a shell.
	payload string
	// splits holds `env -S` command lines; assigns holds env NAME=value
	// operands (also accepted by sudo).
	splits  []string
	assigns []string
}

// wrapperAt reads the wrapper that starts at tokens[i]. ok is false when the
// token is not a wrapper (or is a lookup such as `command -v`).
func wrapperAt(tokens []string, i int) (wrapperStep, bool) {
	name := commandName(tokens[i])
	step := wrapperStep{name: name, floor: Safe, next: i + 1}
	priv := privilegedWrappers[name]
	switch {
	case name == "command" && commandIsLookup(tokens[i+1:]):
		return step, false
	case priv || execWrappers[name]:
		if priv {
			step.floor = SystemWrite
		}
		step.readOptions(tokens, i+1)
		return step, true
	}
	return subcommandWrapper(name, tokens, i)
}

// readOptions walks the wrapper's options and fixed operands and leaves
// step.next at the wrapped command.
func (step *wrapperStep) readOptions(tokens []string, i int) {
	name := step.name
	spec := wrapperSpecs[name]
	from := i
loop:
	for i < len(tokens) {
		t := tokens[i]
		switch {
		case t == "--":
			i++
			break loop
		case strings.HasPrefix(t, "-") && t != "-":
			opts, next := spec.option(tokens, i)
			for _, o := range opts {
				switch {
				case o.is("--split-string") && name == "env":
					step.splits = append(step.splits, o.value)
				case o.has && o.is("--command") && wrapperCarriesCommand(name):
					step.payload = o.value
				}
			}
			i = next
		case name == "env" && isAssignment(t), name == "sudo" && isAssignment(t):
			step.assigns = append(step.assigns, t)
			i++
		case (name == "timeout" || name == "nice" || name == "ionice") && isNumericish(t):
			i++ // timeout 5s / nice 10
		default:
			break loop
		}
	}
	for k := 0; k < wrapperOperands[name] && i < len(tokens); k++ {
		i++
	}
	switch name {
	case "flock":
		// `flock FILE -c COMMAND` carries the command after the lock file.
		if step.payload == "" && i < len(tokens) && strings.HasPrefix(tokens[i], "-") {
			opts, next := spec.option(tokens, i)
			for _, o := range opts {
				if o.has && o.is("--command") {
					step.payload = o.value
					i = next
				}
			}
		}
	case "script":
		// Without -c the BSD form runs the operands after the typescript
		// file; either way the wrapper starts a shell and writes a file.
		step.floor = worstOf(step.floor, CodeExecution)
	case "watch":
		step.watchPayload(tokens, from, i)
		return
	case "parallel", "sem":
		if name == "sem" {
			// sem was an unknown verb before; keep that floor so unwrapping
			// it exposes the inner command without lowering a verdict.
			step.floor = worstOf(step.floor, Unknown)
		}
		step.parallelPayload(tokens, from, i)
	}
	if step.payload != "" {
		step.floor = worstOf(step.floor, CodeExecution)
		i = len(tokens)
	}
	step.next = i
}

// watchPayload decides how watch's command words run. Without -x/--exec,
// watch joins them and hands the string to `sh -c`, so a word carrying shell
// syntax is a command line and is analyzed as one; plain words are the
// command itself.
func (step *wrapperStep) watchPayload(tokens []string, from, i int) {
	step.next = i
	exec := false
	for _, t := range tokens[from:i] {
		if t == "--exec" || (strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--") && strings.ContainsRune(t, 'x')) {
			exec = true
		}
	}
	if exec || i >= len(tokens) {
		return
	}
	for _, t := range tokens[i:] {
		if strings.ContainsAny(t, " \t\n;|&<>()$`") {
			step.payload = strings.Join(tokens[i:], " ")
			step.floor = worstOf(step.floor, CodeExecution)
			step.next = len(tokens)
			return
		}
	}
}

// parallelPayload handles GNU parallel and sem. Unless -q/--quote is given
// they join their command words with spaces and run the line through a shell,
// so a word spelled like a separator (`parallel echo ';' rm -rf ~`) is a real
// separator there. The joined words, up to the first input-source marker
// (`:::`, `::::`), are analyzed as a command line. With -q/--quote the words
// stay literal.
func (step *wrapperStep) parallelPayload(tokens []string, from, i int) {
	for _, t := range tokens[from:i] {
		if t == "--quote" || (strings.HasPrefix(t, "--q") && strings.HasPrefix("--quote", t)) ||
			(strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--") && strings.ContainsRune(t, 'q')) {
			return
		}
	}
	words := tokens[i:]
	for k, t := range words {
		if strings.HasPrefix(t, ":::") {
			words = words[:k]
			break
		}
	}
	for _, t := range words {
		if operatorLookalikes[t] || strings.Contains(t, "\n") {
			step.payload = strings.Join(words, " ")
			return
		}
	}
}

// subcommandWrapper recognises tools that run a command only for certain
// subcommands: `asdf exec`, `direnv exec DIR`, `mise|rtx exec|x … --`,
// `nix run|shell|develop`, and `nix-shell`.
func subcommandWrapper(name string, tokens []string, i int) (wrapperStep, bool) {
	step := wrapperStep{name: name, floor: Safe, next: len(tokens)}
	rest := tokens[i+1:]
	switch name {
	case "asdf":
		if len(rest) > 0 && rest[0] == "exec" {
			step.next = i + 2
			if step.next < len(tokens) && tokens[step.next] == "--" {
				step.next++
			}
			return step, true
		}
	case "direnv":
		// The directory's .envrc is evaluated before the command runs.
		if len(rest) > 0 && rest[0] == "exec" {
			step.floor = CodeExecution
			step.next = min(i+3, len(tokens))
			if step.next < len(tokens) && tokens[step.next] == "--" {
				step.next++
			}
			return step, true
		}
	case "mise", "rtx":
		if len(rest) > 0 && (rest[0] == "exec" || rest[0] == "x") {
			step.floor = CodeExecution
			for j := i + 2; j < len(tokens); j++ {
				t := tokens[j]
				if t == "--" {
					step.next = j + 1
					return step, true
				}
				if t == "-c" || t == "--command" {
					if j+1 < len(tokens) {
						step.payload = tokens[j+1]
					}
					return step, true
				}
				if v, ok := strings.CutPrefix(t, "--command="); ok {
					step.payload = v
					return step, true
				}
				if t == "-C" || t == "--cd" || t == "-j" || t == "--jobs" {
					j++
				}
			}
			return step, true
		}
	case "nix-shell":
		step.floor = CodeExecution
		for j := i + 1; j < len(tokens); j++ {
			t := tokens[j]
			if (t == "--run" || t == "--command") && j+1 < len(tokens) {
				step.payload = tokens[j+1]
				return step, true
			}
			for _, prefix := range []string{"--run=", "--command="} {
				if v, ok := strings.CutPrefix(t, prefix); ok {
					step.payload = v
					return step, true
				}
			}
		}
		return step, true
	case "nix":
		for j := i + 1; j < len(tokens); j++ {
			switch tokens[j] {
			case "run":
				step.floor = CodeExecution
				return step, true
			case "shell", "develop":
				step.floor = CodeExecution
				for k := j + 1; k < len(tokens); k++ {
					if tokens[k] == "-c" || tokens[k] == "--command" {
						step.next = k + 1
						return step, true
					}
					if tokens[k] == "--" {
						break
					}
				}
				return step, true
			}
		}
	}
	return step, false
}
