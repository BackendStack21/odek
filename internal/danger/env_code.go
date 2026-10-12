package danger

import "strings"

// envAssignmentCodeBodies returns the command texts that environment
// assignments among tokens make a shell run: PROMPT_COMMAND is a command line
// run before each prompt, PS0/PS1/PS2/PS4 expand command substitutions, and an
// exported function BASH_FUNC_name%%=() { body; } is defined with its body in
// every bash the command starts. Each returned text is to be classified as a
// command of its own.
func envAssignmentCodeBodies(tokens []string) []string {
	var bodies []string
	for _, tok := range tokens {
		key, value, ok := strings.Cut(tok, "=")
		if !ok || value == "" {
			continue
		}
		key = strings.TrimSuffix(key, "+") // PROMPT_COMMAND+='; cmd' appends
		switch {
		case key == "PROMPT_COMMAND":
			bodies = append(bodies, value)
		case key == "PS0" || key == "PS1" || key == "PS2" || key == "PS4":
			_, subs := extractSubstitutions(value)
			bodies = append(bodies, subs...)
		case strings.HasPrefix(key, "BASH_FUNC_"):
			bodies = append(bodies, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "()")))
		}
	}
	return bodies
}

// isCodeVar reports whether the variable holds shell code: its value is run
// by a shell (PROMPT_COMMAND), expands command substitutions (PS0-PS4) or
// defines an exported function (BASH_FUNC_*).
func isCodeVar(name string) bool {
	switch name {
	case "PROMPT_COMMAND", "PS0", "PS1", "PS2", "PS4":
		return true
	}
	return strings.HasPrefix(name, "BASH_FUNC_")
}

// builtinAssignedCodeBodies covers the builtins that assign a variable
// without a NAME=value word: `printf -v NAME FORMAT [ARG...]` stores the
// formatted text, and `read`/`mapfile`/`readarray` store a line of input. The
// returned bodies are command texts to classify as the assigned value would
// be. unknown reports an input that is not a literal here-string, so the
// stored value cannot be known.
func builtinAssignedCodeBodies(name string, args, stage []string) (bodies []string, unknown bool) {
	switch name {
	case "printf":
		for i, a := range args {
			var target string
			var rest []string
			switch {
			case a == "-v" && i+1 < len(args):
				target, rest = args[i+1], args[i+2:]
			case strings.HasPrefix(a, "-v") && len(a) > 2:
				target, rest = a[2:], args[i+1:]
			default:
				continue
			}
			target, _, _ = strings.Cut(target, "[")
			if isCodeVar(target) {
				for _, r := range rest {
					bodies = append(bodies, envValueBodies(target, r)...)
				}
			}
			return bodies, false
		}
	case "read", "mapfile", "readarray":
		var names []string
		for _, a := range args {
			if strings.HasPrefix(a, "<") || strings.HasPrefix(a, ">") {
				break
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			if v, _, _ := strings.Cut(a, "["); isCodeVar(v) {
				names = append(names, v)
			}
		}
		if len(names) == 0 {
			return nil, false
		}
		text, ok := hereStringText(stage)
		if !ok {
			return nil, true
		}
		for _, n := range names {
			bodies = append(bodies, envValueBodies(n, text)...)
		}
	}
	return bodies, false
}

// envValueBodies returns the command texts a value assigned to a code
// variable makes a shell run.
func envValueBodies(name, value string) []string {
	if value == "" {
		return nil
	}
	return envAssignmentCodeBodies([]string{name + "=" + value})
}

// stageCodeBodies returns every command text a stage makes a shell run
// through its environment, builtin assignments and arithmetic operands:
// code-variable assignments (leading, `env -S`, exported), printf -v / read
// into a code variable, and substitutions bash evaluates inside arithmetic
// and subscript operands. The classifier and the denylist both walk these
// bodies, so they cannot drift. unknown reports a stored value that cannot be
// known statically.
func stageCodeBodies(stage []string, un unwrapped) (bodies []string, unknown bool) {
	for _, split := range un.splits {
		bodies = append(bodies, envAssignmentCodeBodies(tokenize(split))...)
	}
	inner := un.inner
	if len(inner) == 0 {
		return append(bodies, envAssignmentCodeBodies(stage)...), false
	}
	name := commandName(inner[0])
	bodies = append(bodies, arithmeticOperandSubstitutions(name, inner[1:])...)
	envTokens := stage[:len(stage)-len(inner)]
	switch name {
	case "export", "declare", "typeset", "readonly", "local":
		envTokens = append(append([]string(nil), envTokens...), inner[1:]...)
	}
	bodies = append(bodies, envAssignmentCodeBodies(envTokens)...)
	builtin, builtinUnknown := builtinAssignedCodeBodies(name, inner[1:], stage)
	return append(bodies, builtin...), builtinUnknown
}
