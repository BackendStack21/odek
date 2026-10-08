package danger

import "strings"

// denylistMatch reports whether any denylist entry matches a command that cmd
// would run. An entry matches when its tokens equal the leading tokens of a
// command word sequence, so `git push` matches `git push origin` but not
// `git push-notes`. The entry is tried against every command position the
// shell would execute, not only the start of the whole line:
//
//   - each ;, &&, || and & segment and each pipe stage;
//   - the command left after leading assignments and execution wrappers
//     (env, command, nohup, timeout, sudo, xargs, …) are stripped;
//   - the program name by its basename, so /usr/bin/git and ./git match `git`;
//   - git with its global options (-C dir, -c k=v, --git-dir …) removed;
//   - grouping and keyword prefixes ( ( , {, !, then, do, …);
//   - shell -c payloads, eval operands, find -exec commands, and the bodies of
//     command and process substitutions, each matched the same way.
//
// Values that only exist at run time (variable expansions, command output)
// are not resolved.
func denylistMatch(cmd string, denylist []string) bool {
	var entries [][]string
	for _, raw := range denylist {
		if toks := canonicalDenyTokens(tokenize(normalizeCommandSpacing(raw))); len(toks) > 0 {
			entries = append(entries, toks)
		}
	}
	if len(entries) == 0 {
		return false
	}
	return denyScan(cmd, entries, 0)
}

func denyScan(cmd string, entries [][]string, depth int) bool {
	return denyScanVars(cmd, entries, depth, nil)
}

// denyScanVars is denyScan with the shell variables earlier commands of the
// enclosing line assigned a statically known value, so `g=git; $g push` and
// `c=push; git $c` are matched as the commands they run. A variable whose
// value is built at run time is not known and its references stay opaque.
func denyScanVars(cmd string, entries [][]string, depth int, inherited map[string]string) bool {
	if depth > maxSubstDepth {
		return false
	}
	vars := make(map[string]string, len(inherited))
	for k, v := range inherited {
		vars[k] = v
	}
	main, subs := normalize(cmd)
	unquoted := unquotedVariableRefs(main)
	for _, segment := range splitSegments(tokenize(main)) {
		stages := splitPipes(segment)
		for _, stage := range stages {
			if denyStage(denyExpand(stage, vars, unquoted), entries, depth) {
				return true
			}
		}
		if len(stages) == 1 {
			denyAssign(stages[0], vars)
		}
	}
	for _, sub := range subs {
		if denyScanVars(sub, entries, depth+1, vars) {
			return true
		}
	}
	return false
}

// denyExpand substitutes known variables into a stage. An unquoted reference
// that is a whole word and whose value holds several words splits into those
// words, as the shell would, so `$cmd` with cmd="git push" is two tokens.
func denyExpand(stage []string, vars map[string]string, unquoted map[string]bool) []string {
	if len(vars) == 0 {
		return stage
	}
	out := make([]string, 0, len(stage))
	for _, tok := range stage {
		if strings.IndexByte(tok, '$') < 0 || isAssignment(tok) {
			out = append(out, tok)
			continue
		}
		if name, end := variableReference(tok, 0); name != "" && end == len(tok) && tok[0] == '$' {
			if value, ok := vars[name]; ok && unquoted[name] && strings.ContainsAny(value, " \t\n") {
				out = append(out, tokenize(value)...)
				continue
			}
		}
		out = append(out, denySubstitute(tok, vars))
	}
	return out
}

// denySubstitute replaces each reference to a known variable inside one word.
func denySubstitute(tok string, vars map[string]string) string {
	var b strings.Builder
	for pos := 0; pos < len(tok); {
		dollar := strings.IndexByte(tok[pos:], '$')
		if dollar < 0 {
			b.WriteString(tok[pos:])
			break
		}
		dollar += pos
		b.WriteString(tok[pos:dollar])
		name, end := variableReference(tok, dollar)
		value, ok := vars[name]
		if name == "" || !ok {
			b.WriteByte('$')
			pos = dollar + 1
			continue
		}
		b.WriteString(value)
		pos = end
	}
	return b.String()
}

// denyAssign records the assignments of a stage that only assigns (also
// through export/declare/readonly/local). A value that still holds a
// reference or a command-output marker is unknown, and so is any earlier
// value of that name.
func denyAssign(stage []string, vars map[string]string) {
	if len(stage) > 0 {
		switch stage[0] {
		case "export", "declare", "typeset", "readonly", "local":
			stage = stage[1:]
		}
	}
	if len(stage) > 0 {
		switch stage[0] {
		case "read", "mapfile", "readarray", "getopts", "unset":
			// These rebind or remove the named variables at run time.
			for _, name := range stage[1:] {
				delete(vars, name)
			}
			return
		}
	}
	for _, tok := range stage {
		if !isAssignment(tok) {
			if tok == dynamicSubstToken {
				// `name=$(cmd)` leaves the marker as a word after `name=`.
				for _, t := range stage {
					if isAssignment(t) {
						name, _, _ := strings.Cut(t, "=")
						delete(vars, name)
					}
				}
			}
			if strings.HasPrefix(tok, "-") {
				continue
			}
			return
		}
	}
	for _, tok := range stage {
		if !isAssignment(tok) {
			continue
		}
		name, value, _ := strings.Cut(tok, "=")
		value = denySubstitute(value, vars)
		if strings.ContainsAny(value, "$`") || strings.Contains(value, dynamicSubstToken) || len(value) > maxStaticWordBytes {
			delete(vars, name)
			continue
		}
		vars[name] = value
	}
}

// denyGroupWords are shell grammar words that may precede a command in the
// same segment without being part of it.
var denyGroupWords = map[string]bool{
	"{": true, "!": true, "if": true, "then": true, "else": true, "elif": true,
	"do": true, "while": true, "until": true,
}

// denyPeel removes grouping punctuation and grammar words around a stage so
// `(git push)`, `{ git push; }` and `then git push` expose the command.
func denyPeel(stage []string) []string {
	out := append([]string(nil), stage...)
	for len(out) > 0 {
		first := strings.TrimLeft(out[0], "(")
		if first != out[0] {
			out[0] = first
			if first == "" {
				out = out[1:]
			}
			continue
		}
		if denyGroupWords[out[0]] {
			out = out[1:]
			continue
		}
		break
	}
	if n := len(out); n > 0 {
		out[n-1] = strings.TrimRight(out[n-1], ")")
		if out[n-1] == "" || out[n-1] == "}" {
			out = out[:n-1]
		}
	}
	return out
}

func denyStage(stage []string, entries [][]string, depth int) bool {
	stage = denyPeel(stage)
	if len(stage) == 0 {
		return false
	}
	if denyMatchesAny(stage, entries) || denyPayloads(stage, entries, depth) {
		return true
	}
	// unwrapWrappersFull strips every stacked wrapper and leading assignment
	// and surfaces the command strings a wrapper hands to a shell
	// (`watch 'git push'`, `script -c`, `nix-shell --run`), which must be
	// matched as command lines of their own.
	un := unwrapWrappersFull(stage)
	for _, payload := range un.payloads {
		if denyScan(payload, entries, depth+1) {
			return true
		}
	}
	// `env -S 'git push'` runs the split string as the command, ahead of any
	// remaining operands.
	for _, split := range un.splits {
		if denyStage(append(tokenize(split), un.inner...), entries, depth+1) {
			return true
		}
	}
	inner := denyPeel(un.inner)
	if len(inner) == 0 || len(inner) == len(stage) {
		return false
	}
	return denyMatchesAny(inner, entries) || denyPayloads(inner, entries, depth)
}

// denyPayloads matches commands carried inside a command's arguments.
func denyPayloads(inner []string, entries [][]string, depth int) bool {
	name := commandName(inner[0])
	switch {
	case pipedShells[name]:
		if idx := shellInlineScriptIndex(inner); idx >= 0 && inner[idx] != "" {
			return denyScan(inner[idx], entries, depth+1)
		}
	case name == "eval" && len(inner) > 1:
		return denyScan(strings.Join(inner[1:], " "), entries, depth+1)
	case name == "git":
		if payload := gitSubmoduleForeachInner(inner); payload != "" {
			return denyScan(payload, entries, depth+1)
		}
	case name == "find" || name == "fd" || name == "fdfind":
		for i := 1; i < len(inner); i++ {
			switch inner[i] {
			case "-exec", "-execdir", "-ok", "-okdir", "--exec", "--exec-batch", "-x", "-X":
				end := len(inner)
				for j := i + 1; j < len(inner); j++ {
					if inner[j] == ";" || inner[j] == `\;` || inner[j] == "+" {
						end = j
						break
					}
				}
				if denyStage(inner[i+1:end], entries, depth+1) {
					return true
				}
			}
		}
	}
	// A wrapper operand that is itself a whole command line (`watch 'git push'`).
	if len(inner) > 0 && strings.ContainsAny(inner[0], " \t") {
		return denyScan(inner[0], entries, depth+1)
	}
	return false
}

func denyMatchesAny(cand []string, entries [][]string) bool {
	if len(cand) == 0 {
		return false
	}
	canon := canonicalDenyTokens(cand)
	for _, entry := range entries {
		if len(canon) >= len(entry) {
			match := true
			for i := range entry {
				if canon[i] != entry[i] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

// denyGlobalFlags are the value-taking options of each tool that may precede
// its subcommand. docker-style and kubectl-style tables are shared with the
// classifier; the rest are the few other tools whose subcommand follows
// options.
var denyGlobalFlags = map[string]map[string]bool{
	"docker":    containerGlobalFlagsWithArg,
	"podman":    containerGlobalFlagsWithArg,
	"nerdctl":   containerGlobalFlagsWithArg,
	"kubectl":   infraFlagsWithValue["kubectl"],
	"helm":      infraFlagsWithValue["helm"],
	"npm":       {"--prefix": true, "-w": true, "--workspace": true, "--registry": true, "--userconfig": true, "--globalconfig": true, "--cache": true, "--loglevel": true},
	"cargo":     {"--config": true, "-C": true, "-Z": true, "--color": true},
	"terraform": {"-chdir": true},
	"tofu":      {"-chdir": true},
}

// denyStripGlobals removes a tool's global options (and the values they
// consume) from between the program and its subcommand, so `docker -H h push`
// compares as `docker push`. A rustup toolchain selector (`cargo +nightly`)
// is dropped as well.
func denyStripGlobals(name string, toks []string) []string {
	if name == "gh" {
		return denyStripGH(toks)
	}
	withValue, known := denyGlobalFlags[name]
	if !known {
		return toks
	}
	out := []string{toks[0]}
	i := 1
	for ; i < len(toks); i++ {
		t := toks[i]
		switch {
		case name == "cargo" && strings.HasPrefix(t, "+"):
		case t == "--" || !strings.HasPrefix(t, "-") || t == "-":
			return append(out, toks[i:]...)
		case strings.Contains(t, "="):
		case withValue[t]:
			i++
		}
	}
	return out
}

// denyStripGH removes gh's repository and host options ahead of the command.
func denyStripGH(toks []string) []string {
	out := []string{toks[0]}
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if !strings.HasPrefix(t, "-") || t == "-" || t == "--" {
			return append(out, toks[i:]...)
		}
		if takesNext, _ := ghTakesValue(t); takesNext {
			i++
		}
	}
	return out
}

// canonicalDenyTokens reduces a command word sequence to the form entries are
// compared in: the program by basename, and for git the subcommand directly
// after the program with global options removed; other tools lose their global
// options the same way.
func canonicalDenyTokens(toks []string) []string {
	if len(toks) == 0 {
		return nil
	}
	out := append([]string(nil), toks...)
	out[0] = commandName(out[0])
	if out[0] == "git" {
		if sub, args := gitSubcommandAndArgs(out); sub != "" {
			out = append([]string{"git", sub}, args...)
		}
		return out
	}
	return denyStripGlobals(out[0], out)
}
