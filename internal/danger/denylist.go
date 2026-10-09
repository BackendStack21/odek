// Denylist matching. A denylist entry is a sequence of command tokens, and it
// matches a leading token sequence at any command position (segments, pipe
// stages, wrappers, shell -c payloads, eval operands, find -exec, command and
// process substitutions, compound command bodies, env -S payloads), after tool
// global options are stripped and statically known variables are resolved. It
// is never a raw string prefix of the whole line, so `rm -rf /` does not match
// `rm -rf /tmp`, and a differently spelled flag (`rm -fr`) is its own entry.

package danger

import (
	"sort"
	"strings"
)

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
	return denyScan(cmd, &denyCtx{entries: entries, seen: map[string]int{}}, 0)
}

// denyCtx carries the compiled entries and the set of command lines and
// stages already examined during one denylistMatch call. Scanning is a pure
// function of its input and, where the depth limit cuts nested payloads off,
// of the depth it is reached at. A repeat can only reproduce a miss (a hit
// would have ended the scan) unless the earlier visit was cut short by the
// depth limit and the repeat is shallower. Skipping the other repeats keeps
// nested eval operands and repeated find -exec operands linear in the command
// size instead of re-scanning the same text once per path that reaches it.
type denyCtx struct {
	entries [][]string
	// seen maps a visited key to the depth of its visit; complete visits are
	// recorded as 0 so no later visit can be shallower.
	seen map[string]int
	// cuts counts the scans the depth limit has refused so far.
	cuts int
	// stageEntries counts every denyStage call and stageScans the ones the
	// memo let through; both must stay linear in the number of command
	// stages on hostile input, since even a deduplicated entry builds an
	// O(n) memo key.
	stageEntries int
	stageScans   int
}

// firstVisit records key at depth and reports whether it must be scanned: it
// was never visited, or only at a greater depth by a visit the depth limit cut
// short. The returned mark is passed to settle once the scan has missed.
func (dc *denyCtx) firstVisit(key string, depth int) (scan bool, mark int) {
	if prior, dup := dc.seen[key]; dup && prior <= depth {
		return false, 0
	}
	dc.seen[key] = depth
	return true, dc.cuts
}

// settle marks key's visit complete when the depth limit refused nothing while
// it ran, so a shallower repeat could not reach anything new.
func (dc *denyCtx) settle(key string, mark int) {
	if dc.cuts == mark {
		dc.seen[key] = 0
	}
}

func denyScan(cmd string, dc *denyCtx, depth int) bool {
	return denyScanVars(cmd, dc, depth, nil)
}

// denyScanVars is denyScan with the shell variables earlier commands of the
// enclosing line assigned a statically known value, so `g=git; $g push` and
// `c=push; git $c` are matched as the commands they run. A variable whose
// value is built at run time is not known and its references stay opaque.
func denyScanVars(cmd string, dc *denyCtx, depth int, inherited map[string]string) bool {
	if depth > maxSubstDepth {
		dc.cuts++
		return false
	}
	vars := make(map[string]string, len(inherited))
	for k, v := range inherited {
		vars[k] = v
	}
	scanKey := "scan\x00" + cmd + "\x00" + denyVarsKey(inherited)
	scan, mark := dc.firstVisit(scanKey, depth)
	if !scan {
		return false
	}
	defer dc.settle(scanKey, mark)
	main, subs := normalize(cmd)
	unquoted := unquotedVariableRefs(main)
	tokens, ops, _ := tokenizeMarked(main)
	for _, segment := range splitSegments(markLiteralOperators(tokens, ops)) {
		stages := splitPipes(segment)
		var earlier [][]string
		for i, stage := range stages {
			expanded := denyExpand(stage, vars, unquoted)
			if denyStage(expanded, dc, depth) {
				return true
			}
			// A shell fed by a static echo/printf runs that text as a script.
			if i > 0 && denyStaticPipeFeed(earlier, expanded, dc, depth) {
				return true
			}
			earlier = append(earlier, expanded)
		}
		if len(stages) == 1 {
			denyAssign(stages[0], vars)
		}
	}
	// Commands inside loops, conditionals, case arms, groups and function
	// bodies are command positions of their own.
	for _, stage := range commandStages(tokens, ops) {
		if denyStage(denyExpand(stage, vars, unquoted), dc, depth) {
			return true
		}
	}
	for _, sub := range subs {
		if denyScanVars(sub, dc, depth+1, vars) {
			return true
		}
	}
	return false
}

// denyVarsKey renders a variable map in a stable order.
func denyVarsKey(vars map[string]string) string {
	if len(vars) == 0 {
		return ""
	}
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vars[k])
		b.WriteByte(0)
	}
	return b.String()
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
			if tok == dynamicSubstToken || tok == procSubstToken {
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

func denyStage(stage []string, dc *denyCtx, depth int) bool {
	stage = denyPeel(stage)
	if len(stage) == 0 {
		return false
	}
	dc.stageEntries++
	stageKey := "stage\x00" + strings.Join(stage, "\x00")
	scan, mark := dc.firstVisit(stageKey, depth)
	if !scan {
		return false
	}
	dc.stageScans++
	defer dc.settle(stageKey, mark)
	if denyMatchesAny(stage, dc.entries) || denyPayloads(stage, dc, depth) {
		return true
	}
	// unwrapWrappersFull strips every stacked wrapper and leading assignment
	// and surfaces the command strings a wrapper hands to a shell
	// (`watch 'git push'`, `script -c`, `nix-shell --run`), which must be
	// matched as command lines of their own.
	un := unwrapWrappersFull(stage)
	for _, payload := range un.payloads {
		if denyScan(payload, dc, depth+1) {
			return true
		}
	}
	// `env -S 'git push'` runs the split string as the command, ahead of any
	// remaining operands.
	for _, split := range un.splits {
		if denyStage(append(tokenize(split), un.inner...), dc, depth+1) {
			return true
		}
	}
	inner := denyPeel(un.inner)
	if len(inner) == 0 || len(inner) == len(stage) {
		return false
	}
	return denyMatchesAny(inner, dc.entries) || denyPayloads(inner, dc, depth)
}

// denyStaticPipeFeed scans the text a static producer pipes into a shell, which
// the shell executes as commands. The producer is an echo/printf stage, or a
// pass-through stage carrying a here-string, reached by walking back from the
// shell through pass-through stages that hand their input on unchanged.
func denyStaticPipeFeed(upstream [][]string, sink []string, dc *denyCtx, depth int) bool {
	cmd, _ := unwrapWrappers(sink)
	if len(cmd) == 0 || !pipedShells[commandName(cmd[0])] || shellInlineScriptIndex(cmd) >= 0 {
		return false
	}
	for j := len(upstream) - 1; j >= 0; j-- {
		stage := upstream[j]
		var text string
		var ok bool
		if passThroughStage(stage) {
			// A here-string replaces the piped input of its stage.
			if text, ok = hereStringText(stage); !ok {
				continue
			}
		} else if text, ok = staticPipeText([][]string{stage}); !ok {
			return false
		}
		text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", " "))
		return text != "" && denyScan(text, dc, depth+1)
	}
	return false
}

// hereStringText returns the word a stage reads as its here-string.
func hereStringText(stage []string) (string, bool) {
	for i := 0; i+1 < len(stage); i++ {
		if stage[i] == "<<<" {
			return stage[i+1], true
		}
	}
	return "", false
}

// passThroughStage reports whether a pipe stage writes the data it reads (or
// its here-string) to standard output unchanged, or a line-subset of it: cat
// and tee, and the line filters sort, uniq, tac, head and tail. A file operand
// makes the stage read that file instead of standard input, so only flags (and
// the numeric counts of head/tail) are allowed; tee's operands are output files.
// Anything that rewrites the bytes (tr, sed, awk, base64, ...) is not listed.
func passThroughStage(stage []string) bool {
	cmd, _ := unwrapWrappers(stage)
	if len(cmd) == 0 {
		return false
	}
	name := commandName(cmd[0])
	switch name {
	case "cat", "tee", "sort", "uniq", "tac", "head", "tail":
	default:
		return false
	}
	for i := 1; i < len(cmd); i++ {
		tok := cmd[i]
		switch {
		case tok == "<<<":
			i++ // the here-string word
		case strings.HasPrefix(tok, "-"):
		case name == "tee":
		case (name == "head" || name == "tail") && isAllDigits(tok):
		default:
			return false
		}
	}
	return true
}

// denyPayloads matches commands carried inside a command's arguments.
func denyPayloads(inner []string, dc *denyCtx, depth int) bool {
	name := commandName(inner[0])
	switch {
	case pipedShells[name]:
		if idx := shellInlineScriptIndex(inner); idx >= 0 && inner[idx] != "" {
			return denyScan(inner[idx], dc, depth+1)
		}
		// A here-string is the script the shell reads from standard input.
		for i := 1; i+1 < len(inner); i++ {
			if inner[i] == "<<<" && inner[i+1] != "" && denyScan(inner[i+1], dc, depth+1) {
				return true
			}
		}
	case name == "eval" && len(inner) > 1:
		return denyScan(strings.Join(inner[1:], " "), dc, depth+1)
	case name == "git":
		if payload := gitSubmoduleForeachInner(inner); payload != "" {
			return denyScan(payload, dc, depth+1)
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
				if denyStage(inner[i+1:end], dc, depth+1) {
					return true
				}
				// The payload just scanned carries every -exec nested
				// inside it, so the outer loop resumes after it; re-entering
				// each inner position would build a memo key per position
				// and make a long chain quadratic.
				i = end
			}
		}
	}
	// A wrapper operand that is itself a whole command line (`watch 'git push'`).
	if len(inner) > 0 && strings.ContainsAny(inner[0], " \t") {
		return denyScan(inner[0], dc, depth+1)
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
