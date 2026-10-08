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
	if depth > maxSubstDepth {
		return false
	}
	main, subs := normalize(cmd)
	for _, segment := range splitSegments(tokenize(main)) {
		for _, stage := range splitPipes(segment) {
			if denyStage(stage, entries, depth) {
				return true
			}
		}
	}
	for _, sub := range subs {
		if denyScan(sub, entries, depth+1) {
			return true
		}
	}
	return false
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

// canonicalDenyTokens reduces a command word sequence to the form entries are
// compared in: the program by basename, and for git the subcommand directly
// after the program with global options removed.
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
	}
	return out
}
