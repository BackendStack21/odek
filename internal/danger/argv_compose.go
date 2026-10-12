package danger

import "strings"

// maxComposedCommands bounds how many command lines one argv composer
// expands into; a longer input is cut off.
const maxComposedCommands = 256

// argvComposerName returns the argv composer (xargs, parallel, xe) a stage
// runs, or "".
func argvComposerName(stage []string) string {
	for _, tok := range stage {
		if name := commandName(tok); argvComposers[name] {
			return name
		}
	}
	return ""
}

// xargsReplaceString returns the placeholder an xargs-style option list
// defines (-I STR, -ISTR, -i, -iSTR, --replace[=STR]), or "".
func xargsReplaceString(opts []string) string {
	ph := ""
	for i := 0; i < len(opts); i++ {
		o := opts[i]
		switch {
		case o == "-I" && i+1 < len(opts):
			ph = opts[i+1]
			i++
		case strings.HasPrefix(o, "-I") && len(o) > 2:
			ph = o[2:]
		case o == "-i" || o == "--replace":
			ph = "{}"
		case strings.HasPrefix(o, "-i") && !strings.HasPrefix(o, "--") && len(o) > 2:
			ph = o[2:]
		case strings.HasPrefix(o, "--replace="):
			ph = strings.TrimPrefix(o, "--replace=")
		}
	}
	return ph
}

// splitParallelSources splits a parallel command line at its `:::` argument
// source: the command words before it and the literal arguments after it
// (up to the next source marker).
func splitParallelSources(inner []string) (cmd, args []string, found bool) {
	for i, tok := range inner {
		if tok == ":::" {
			rest := inner[i+1:]
			for j, r := range rest {
				if strings.HasPrefix(r, ":::") {
					rest = rest[:j]
					break
				}
			}
			return inner[:i], rest, true
		}
	}
	return inner, nil, false
}

// argvCompositions returns the command lines an argv composer (xargs, GNU
// parallel, xe) runs given its statically known input: the standard input
// text when haveStdin is set, and the `:::` argument sources written on the
// parallel command line. xargs appends the input words to its command once
// (or substitutes each input line for its -I placeholder); parallel runs its
// command once per argument, substitutes `{}`, and with no command at all
// runs each input line as a command of its own. Redirects on the stage are
// not part of the composed command.
func argvCompositions(stage []string, stdin string, haveStdin bool) [][]string {
	idx := -1
	for i, tok := range stage {
		if argvComposers[commandName(tok)] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	name := commandName(stage[idx])
	inner, ok := argvComposerInnerCommand(stage)
	if !ok {
		return nil
	}
	optEnd := len(stage) - len(inner)
	var opts []string
	if optEnd > idx+1 {
		opts = stage[idx+1 : optEnd]
	}
	inner = redirectFreeArguments(inner)
	ph := ""
	switch name {
	case "parallel":
		ph = "{}"
	default:
		ph = xargsReplaceString(opts)
	}
	cmd, sources, hasSources := inner, []string(nil), false
	if name == "parallel" {
		cmd, sources, hasSources = splitParallelSources(inner)
	}
	var lines, words []string
	switch {
	case hasSources:
		lines = sources
	case haveStdin:
		for _, l := range strings.Split(stdin, "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, strings.TrimRight(l, "\r"))
			}
		}
		words = strings.FieldsFunc(stdin, func(r rune) bool { return r == 0 || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	default:
		return nil
	}
	var out [][]string
	emit := func(c []string) {
		if len(out) < maxComposedCommands {
			out = append(out, c)
		}
	}
	placeholder := ph != "" && anyContains(cmd, ph)
	switch {
	case len(cmd) == 0:
		if name == "parallel" {
			for _, l := range lines {
				emit(tokenize(l))
			}
		}
	case placeholder:
		for _, l := range lines {
			var c []string
			for _, tok := range cmd {
				switch {
				case tok == ph:
					if split := tokenize(l); len(split) > 0 {
						c = append(c, split...)
					} else {
						c = append(c, l)
					}
				default:
					c = append(c, strings.ReplaceAll(tok, ph, l))
				}
			}
			emit(c)
		}
	case name == "parallel":
		for _, l := range lines {
			emit(append(append([]string(nil), cmd...), l))
		}
	default:
		emit(append(append([]string(nil), cmd...), words...))
	}
	return out
}

func anyContains(tokens []string, sub string) bool {
	for _, t := range tokens {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}
