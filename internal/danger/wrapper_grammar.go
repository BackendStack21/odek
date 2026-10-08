package danger

import "strings"

// wrapperSpec describes the option grammar of a wrapper that runs another
// command: which options consume a value (so the value is not mistaken for
// the wrapped command), how many fixed operands precede the command, and
// which option carries a shell command string instead of an argv.
type wrapperSpec struct {
	// short lists the single-letter options that take a value, either fused
	// into the cluster (`-oL`, `-sKILL`) or in the next token (`-o L`).
	short string
	// exact lists multi-letter single-dash options that take a value.
	exact []string
	// long maps each long option to whether it takes a value. Unambiguous
	// prefixes of a listed name are accepted as getopt_long does.
	long map[string]bool
	// operands is the count of positional operands (priority, CPU list, lock
	// file) that precede the wrapped command.
	operands int
	// payloadShort and payloadLong name the option whose value is a command
	// string the wrapper hands to a shell.
	payloadShort byte
	payloadLong  string
}

var wrapperSpecs = map[string]wrapperSpec{
	"timeout": {short: "sk", long: map[string]bool{"signal": true, "kill-after": true, "foreground": false, "preserve-status": false, "verbose": false}},
	"stdbuf":  {short: "ioe", long: map[string]bool{"input": true, "output": true, "error": true}},
	"nice":    {short: "n", long: map[string]bool{"adjustment": true}},
	"ionice":  {short: "cnpPu", long: map[string]bool{"class": true, "classdata": true, "pid": true, "pgid": true, "uid": true, "ignore": false}},
	"chrt": {short: "TPD", operands: 1, long: map[string]bool{"pid": false, "sched-runtime": true, "sched-period": true, "sched-deadline": true,
		"batch": false, "deadline": false, "fifo": false, "idle": false, "other": false, "rr": false, "reset-on-fork": false, "max": false, "all-tasks": false, "verbose": false}},
	"taskset": {operands: 1, long: map[string]bool{"cpu-list": false, "pid": false, "all-tasks": false}},
	"flock": {short: "wEc", operands: 1, payloadShort: 'c', payloadLong: "command",
		long: map[string]bool{"timeout": true, "wait": true, "conflict-exit-code": true, "command": true, "nonblock": false, "nb": false, "shared": false, "exclusive": false, "unlock": false, "close": false, "no-fork": false, "verbose": false}},
	"script": {short: "cEIOBTmo", operands: 1, payloadShort: 'c', payloadLong: "command",
		long: map[string]bool{"command": true, "echo": true, "log-in": true, "log-out": true, "log-io": true, "log-timing": true, "logging-format": true, "output-limit": true,
			"append": false, "flush": false, "force": false, "quiet": false, "return": false}},
	"arch":   {exact: []string{"-arch", "-e", "-d"}},
	"watch":  {short: "n", long: map[string]bool{"interval": true, "differences": false, "precise": false, "no-title": false, "beep": false, "errexit": false, "chgexit": false, "color": false, "exec": false, "equexit": true, "no-linewrap": false}},
	"strace": {short: "aAbeEIoOpPsSuX", long: map[string]bool{"output": true, "attach": true, "trace": true}},
	"sudo": {short: "CDghprTtUu", long: map[string]bool{"user": true, "group": true, "chdir": true, "host": true, "prompt": true, "role": true, "type": true,
		"command-timeout": true, "other-user": true, "chroot": true, "close-from": true, "preserve-env": false, "login": false, "shell": false,
		"stdin": false, "non-interactive": false, "background": false, "askpass": false, "edit": false, "help": false, "list": false, "validate": false,
		"version": false, "remove-timestamp": false, "reset-timestamp": false, "set-home": false, "bell": false, "preserve-groups": false}},
	"doas": {short: "uC"},
}

// option parses the dash-prefixed token at tokens[i]. It returns the index
// after the option and its value, and whether the option carries a shell
// command string.
func (s wrapperSpec) option(tokens []string, i int) (next int, value string, payload bool) {
	t := tokens[i]
	take := func(fused string, hasFused bool) (int, string) {
		if hasFused {
			return i + 1, fused
		}
		if i+1 < len(tokens) {
			return i + 2, tokens[i+1]
		}
		return i + 1, ""
	}
	if strings.HasPrefix(t, "--") {
		name, val, hasEq := strings.Cut(t[2:], "=")
		if name == "" {
			return i + 1, "", false
		}
		match, found := "", false
		if _, ok := s.long[name]; ok {
			match, found = name, true
		} else {
			// An unambiguous prefix names the option; when several options
			// share it, a value-taking one wins so its value is not read as
			// the wrapped command.
			for long, takes := range s.long {
				if strings.HasPrefix(long, name) && (!found || (takes && !s.long[match])) {
					match, found = long, true
				}
			}
		}
		if !found {
			return i + 1, "", false
		}
		if s.long[match] {
			next, value = take(val, hasEq)
			return next, value, match == s.payloadLong && s.payloadLong != ""
		}
		return i + 1, "", false
	}
	for _, ex := range s.exact {
		if t == ex {
			next, value = take("", false)
			return next, value, false
		}
	}
	for k := 1; k < len(t); k++ {
		if strings.IndexByte(s.short, t[k]) >= 0 {
			next, value = take(t[k+1:], k+1 < len(t))
			return next, value, s.payloadShort != 0 && t[k] == s.payloadShort
		}
	}
	return i + 1, "", false
}

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
			switch {
			case name == "env":
				if next, val, split, ok := envOptionValue(tokens, i); ok {
					if split {
						step.splits = append(step.splits, val)
					}
					i = next
					continue
				}
				i++
			case argvComposers[name]:
				if xargsValueFlags[t] && i+1 < len(tokens) {
					i += 2
				} else {
					i++
				}
			default:
				next, val, payload := spec.option(tokens, i)
				if payload {
					step.payload = val
				}
				i = next
			}
		case name == "env" && isAssignment(t), name == "sudo" && isAssignment(t):
			step.assigns = append(step.assigns, t)
			i++
		case (name == "timeout" || name == "nice" || name == "ionice") && isNumericish(t):
			i++ // timeout 5s / nice 10
		default:
			break loop
		}
	}
	for k := 0; k < spec.operands && i < len(tokens); k++ {
		i++
	}
	switch name {
	case "flock":
		// `flock FILE -c COMMAND` carries the command after the lock file.
		if step.payload == "" && i < len(tokens) && strings.HasPrefix(tokens[i], "-") {
			if next, val, payload := spec.option(tokens, i); payload {
				step.payload = val
				i = next
			}
		}
	case "script":
		// Without -c the BSD form runs the operands after the typescript
		// file; either way the wrapper starts a shell and writes a file.
		step.floor = worstOf(step.floor, CodeExecution)
	case "watch":
		step.watchPayload(tokens, from, i)
		return
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
