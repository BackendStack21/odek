package danger

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Analysis retains independent effects for policy enforcement. Class is only
// a display summary: a higher-ranked effect cannot authorize a denied sibling.
type Analysis struct {
	Effects        []RiskClass
	ExecutionFiles []string
	// RewrittenFiles are execution files that an earlier stage of the same
	// command writes: no prior read describes the content that will run.
	RewrittenFiles []string
}

func (a Analysis) Class() RiskClass {
	cls := Safe
	for _, effect := range a.Effects {
		cls = worstOf(cls, effect)
	}
	return cls
}

func (a *Analysis) add(cls RiskClass) {
	// An upload is always also egress: the two stay independent policy
	// keys, and a policy that denies egress must still deny the upload.
	if cls == NetworkUpload {
		a.add(NetworkEgress)
	}
	for _, existing := range a.Effects {
		if existing == cls {
			return
		}
	}
	a.Effects = append(a.Effects, cls)
}

func (a *Analysis) addFile(path string) {
	for _, existing := range a.ExecutionFiles {
		if path == existing {
			return
		}
	}
	a.ExecutionFiles = append(a.ExecutionFiles, path)
}

func (a *Analysis) merge(other Analysis) {
	for _, effect := range other.Effects {
		a.add(effect)
	}
	for _, path := range other.ExecutionFiles {
		a.addFile(path)
	}
	for _, path := range other.RewrittenFiles {
		a.addRewritten(path)
	}
}

func (a *Analysis) addRewritten(path string) {
	for _, existing := range a.RewrittenFiles {
		if path == existing {
			return
		}
	}
	a.RewrittenFiles = append(a.RewrittenFiles, path)
}

func stricterAction(a, b Action) Action {
	if a == Deny || b == Deny {
		return Deny
	}
	if a == Prompt || b == Prompt {
		return Prompt
	}
	return Allow
}

// PromptClassForCommand chooses only effects requiring approval. Multiple
// independent prompts use the non-trustable batch class.
func (c *DangerousConfig) PromptClassForCommand(cmd string) RiskClass {
	cls := Safe
	count := 0
	for _, effect := range Analyze(cmd).Effects {
		if c.ActionFor(effect) == Prompt {
			cls = effect
			count++
		}
	}
	if count > 1 {
		return ToolBatchClass
	}
	return cls
}

// Analyze uses the same analysis as Classify, preserving effects across
// substitutions, compound commands, pipelines and command wrappers.
func Analyze(cmd string) Analysis { return analyzeAtDepth(cmd, 0) }

type shellAnalysisState struct {
	cwd       string
	vars      map[string]string
	uncertain bool
	// written holds the resolved paths earlier stages of the command write;
	// it is shared with nested payload analyses.
	written map[string]bool
	// unquoted names the variables the analyzed text references outside any
	// quoting, where the shell word-splits and globs their values.
	unquoted map[string]bool
}

// Bound static expansion independently of recursion: repeated assignments
// can otherwise double a value at each stage without nesting a command.
const maxStaticWordBytes = 64 << 10

func analyzeAtDepth(cmd string, depth int) Analysis { return analyzeWithState(cmd, depth, nil) }

func analyzeWithState(cmd string, depth int, inherited *shellAnalysisState) Analysis {
	var result Analysis
	if isRawBlocked(cmd) {
		result.add(Blocked)
		return result
	}
	if depth > maxSubstDepth {
		result.add(Unknown)
		return result
	}
	main, subs := normalize(cmd)
	tokens := tokenize(main)
	cwd, err := os.Getwd()
	state := shellAnalysisState{cwd: cwd, vars: make(map[string]string), uncertain: err != nil, written: make(map[string]bool)}
	if st, statErr := os.Stat(cwd); statErr != nil || !st.IsDir() {
		state.uncertain = true
	}
	if inherited != nil {
		state.cwd = inherited.cwd
		state.uncertain = inherited.uncertain
		if inherited.written != nil {
			state.written = inherited.written
		}
		for name, value := range inherited.vars {
			state.vars[name] = value
		}
	}
	state.unquoted = unquotedVariableRefs(main)
	segments := splitSegments(tokens)
	operators := segmentOperators(tokens)
	if len(subs) > 0 && hasAny(tokens, "cd", "pushd", "popd") {
		result.add(Unknown)
	}
	// Conditional alternatives and background state cannot be carried as one
	// deterministic cwd/variable snapshot. Stateful stages below fail closed.
	ambiguous := hasAny(tokens, "||", "&")
	// Mutations made behind `&&` only happen when every earlier operand
	// succeeded. Inside the chain they are carried (the rest of the chain only
	// runs once they happened); when the chain ends, the state they touched
	// is unknown.
	chainVars := make(map[string]bool)
	chainCwd := false
	endChain := func() {
		for name := range chainVars {
			delete(state.vars, name)
			delete(chainVars, name)
		}
		if chainCwd {
			state.uncertain = true
			chainCwd = false
		}
	}
	for segmentIndex, segment := range segments {
		afterAnd := operators[segmentIndex] == "&&"
		if !afterAnd {
			endChain()
		}
		stages := splitPipes(segment)
		prepared := make([][]string, 0, len(stages))
		for _, stage := range stages {
			stage = state.expand(stage)
			prepared = append(prepared, stage)
		}
		var pipeline []string
		for i, stage := range prepared {
			legacyStage := append([]string(nil), stage...)
			stageCwd, cwdKnown := wrapperDirectory(stage, state.cwd)
			payloadState := state
			payloadState.cwd = stageCwd
			payloadState.uncertain = state.uncertain || !cwdKnown
			inner, floor := unwrapWrappers(stage)
			if len(inner) > 0 {
				name := commandName(inner[0])
				if pipedShells[name] {
					if idx := shellInlineScriptIndex(inner); idx >= 0 && inner[idx] != "" {
						result.merge(analyzeWithState(inner[idx], depth+1, &payloadState))
						if at := len(stage) - len(inner) + idx; at < len(legacyStage) {
							legacyStage[at] = "echo"
						}
					}
				}
				if name == "git" {
					if payload := gitSubmoduleForeachInner(inner); payload != "" {
						result.merge(analyzeWithState(payload, depth+1, &payloadState))
					}
				}
				if name == "eval" && len(inner) > 1 {
					result.merge(analyzeWithState(strings.Join(inner[1:], " "), depth+1, &payloadState))
				}
			}
			if i > 0 {
				pipeline = append(pipeline, "|")
			}
			pipeline = append(pipeline, legacyStage...)
			// Preserve findings from each stage before pipeline summaries can
			// replace them with a differently configured higher-ranked class.
			result.add(classifyStage(legacyStage, i > 0))
			if floor != Safe {
				result.add(floor)
				if environmentRunsCode(stage[:len(stage)-len(inner)]) {
					result.add(CodeExecution)
				}
			}
			if len(inner) == 0 {
				if len(stages) == 1 {
					if ambiguous {
						state.forget(assignedNames(stage)...)
					} else {
						state.assign(stage)
						if afterAnd {
							for _, assigned := range assignedNames(stage) {
								chainVars[assigned] = true
							}
						}
					}
				}
				continue
			}
			name := commandName(inner[0])
			state.rebind(name, inner)
			if isCodeExecution(name, inner) || explicitUntrustedExecutable(inner[0]) || (i > 0 && (pipedShells[name] || isStdinExecInterpreter(name) || embeddedShellInterpreters[name])) {
				result.add(CodeExecution)
			}
			if isNetworkEgress(name, inner) {
				result.add(NetworkEgress)
			}
			feed := stdinFeed{piped: i > 0}
			if feed.piped {
				_, feed.static = staticPipePayload(prepared[:i])
			}
			for _, effect := range networkTransferEffects(name, inner, feed) {
				result.add(effect)
			}
			if isInstall(name, inner) {
				result.add(Install)
			}
			if isLocalWrite(name, inner) {
				result.add(LocalWrite)
			}
			if isSystemWrite(name, inner) {
				result.add(SystemWrite)
			}
			if isPersistenceWrite(name, inner) {
				result.add(Persistence)
			}
			if !displayVerbs[name] || stageHasOutputRedirect(stage) {
				if touchesSystemPath(inner[1:]) {
					result.add(SystemWrite)
				}
				for _, token := range stage {
					if resource := classifyResourceToken(token); resource != Safe {
						result.add(resource)
					}
				}
			}
			if cwdKnown && !state.uncertain {
				files, rewritten := stageLedgerFiles(stage, stageCwd, state.written)
				for _, path := range files {
					result.addFile(path)
				}
				for _, path := range rewritten {
					result.addRewritten(path)
				}
			}
			for _, target := range semanticWriteTargets(name, inner) {
				if target == "-" {
					continue
				}
				result.add(state.targetRisk(target, stageCwd, cwdKnown))
			}
			for j, tok := range stage {
				if isRedirectToken(tok) && j+1 < len(stage) {
					if (tok == ">&" || tok == ">>&") && isAllDigits(stage[j+1]) {
						continue
					}
					result.add(state.targetRisk(stage[j+1], stageCwd, cwdKnown))
				}
			}
			// Only actual write operands are resolved against a changed cwd;
			// language programs and display strings are analyzed by their adapters.
			if writePrefixes[name] && !commandOnlyReads(name, inner) && name != "sed" {
				for _, tok := range inner[1:] {
					if tok == "" || strings.HasPrefix(tok, "-") || isRedirectToken(tok) {
						continue
					}
					result.add(state.targetRisk(tok, stageCwd, cwdKnown))
				}
			}
			if (stageCwd != cwd || !cwdKnown || state.uncertain) && !displayVerbs[name] {
				for _, tok := range inner[1:] {
					if tok == "" || strings.HasPrefix(tok, "-") || strings.ContainsAny(tok, " \t{}()") || isRedirectToken(tok) {
						continue
					}
					if !cwdKnown || state.uncertain {
						result.add(Unknown)
					} else {
						result.add(classifyResourceToken(resolveInDirectory(tok, stageCwd)))
					}
				}
			}
			if name == "cd" || name == "pushd" || name == "popd" {
				if len(stages) > 1 {
					continue
				}
				wasUncertain := state.uncertain
				state.uncertain = ambiguous || name == "popd"
				if afterAnd {
					chainCwd = true
				}
				path, known := directoryOperand(name, inner[1:])
				if !known || strings.ContainsAny(path, "$*?[]") || path == "-" {
					state.uncertain = true
					continue
				}
				if wasUncertain && !filepath.IsAbs(expandShellTokenPath(path)) {
					// A relative step from an unknown directory stays unknown.
					state.uncertain = true
					continue
				}
				path = resolveInDirectory(path, state.cwd)
				if st, err := os.Stat(path); err != nil || !st.IsDir() {
					state.uncertain = true
				} else {
					state.cwd = path
				}
			}
		}
		result.add(classifyPipeline(pipeline))
	}
	endChain()
	for _, sub := range subs {
		result.merge(analyzeWithState(sub, depth+1, &state))
	}
	if len(result.Effects) == 0 {
		result.add(Safe)
	}
	sort.Slice(result.Effects, func(i, j int) bool { return Rank(result.Effects[i]) > Rank(result.Effects[j]) })
	return result
}

func environmentRunsCode(prefix []string) bool {
	for _, tok := range prefix {
		if !isAssignment(tok) {
			continue
		}
		name, _, _ := strings.Cut(tok, "=")
		name = strings.ToUpper(name)
		if envExecNames[name] || strings.HasSuffix(name, "PAGER") || name == "ENV" || name == "SHELL" {
			return true
		}
	}
	return false
}

// expand substitutes the statically known shell variables into tokens. Each
// token is scanned once for `$` and names are looked up in the variable map,
// so the cost is linear in the command length regardless of how many
// variables are known. Substituted values are not rescanned.
func (s *shellAnalysisState) expand(tokens []string) []string {
	out := append([]string(nil), tokens...)
	separators := " \t\n\r*?["
	if ifs, ok := s.vars["IFS"]; ok {
		separators += ifs
	}
	for i, token := range out {
		if strings.IndexByte(token, '$') >= 0 {
			token = s.expandToken(token, isAssignment(tokens[i]), separators)
		}
		if len(token) > maxStaticWordBytes {
			token = dynamicSubstToken
		}
		if token == dynamicSubstToken && isAssignment(tokens[i]) {
			name, _, _ := strings.Cut(tokens[i], "=")
			token = name + "=" + dynamicSubstToken
		}
		out[i] = token
	}
	return out
}

// expandToken substitutes known variables into one token. An unquoted
// reference whose value the shell would word-split or glob cannot be one
// operand, so the whole token fails closed to the dynamic marker.
func (s *shellAnalysisState) expandToken(token string, assignment bool, separators string) string {
	var b strings.Builder
	for pos := 0; pos < len(token); {
		dollar := strings.IndexByte(token[pos:], '$')
		if dollar < 0 {
			b.WriteString(token[pos:])
			break
		}
		dollar += pos
		b.WriteString(token[pos:dollar])
		name, end := variableReference(token, dollar)
		if name == "" {
			b.WriteByte('$')
			pos = dollar + 1
			continue
		}
		value, known := s.vars[name]
		if !known {
			b.WriteString(token[dollar:end])
			pos = end
			continue
		}
		if !assignment && s.unquoted[name] && strings.ContainsAny(value, separators) {
			return dynamicSubstToken
		}
		if b.Len()+len(value) > maxStaticWordBytes {
			return dynamicSubstToken
		}
		b.WriteString(value)
		pos = end
	}
	return b.String()
}

// variableReference parses `$name` or `${name}` at token[dollar] and returns
// the variable name and the index just past the reference; an empty name
// means the `$` does not start a plain variable reference.
func variableReference(token string, dollar int) (name string, end int) {
	start := dollar + 1
	if start < len(token) && token[start] == '{' {
		closing := strings.IndexByte(token[start:], '}')
		if closing < 0 {
			return "", 0
		}
		name = token[start+1 : start+closing]
		for j := 0; j < len(name); j++ {
			if !isShellVarByte(name[j]) {
				return "", 0
			}
		}
		return name, start + closing + 1
	}
	end = start
	for end < len(token) && isShellVarByte(token[end]) {
		end++
	}
	return token[start:end], end
}

// unquotedVariableRefs returns the variables referenced outside single and
// double quotes, where the shell splits and globs the expanded value.
func unquotedVariableRefs(text string) map[string]bool {
	refs := make(map[string]bool)
	inSingle, inDouble := false, false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '\\' && !inSingle:
			i++
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == '$' && !inSingle && !inDouble:
			if name, end := variableReference(text, i); name != "" {
				refs[name] = true
				i = end - 1
			}
		}
	}
	return refs
}

// segmentOperators returns, for each segment splitSegments produces, the
// separator that precedes it ("" for the first). A newline right after `&&`
// or `||` continues the list, so it keeps the earlier operator.
func segmentOperators(tokens []string) []string {
	var ops []string
	pending, current := "", ""
	inSegment := false
	for _, tok := range tokens {
		switch tok {
		case ";", "&&", "||", "&":
			if inSegment {
				ops = append(ops, current)
				inSegment = false
			} else if tok == ";" && (pending == "&&" || pending == "||") {
				continue
			}
			pending = tok
		default:
			if !inSegment {
				current = pending
				inSegment = true
			}
		}
	}
	if inSegment {
		ops = append(ops, current)
	}
	return ops
}

// assignedNames lists the variable names bound by the NAME=value words.
func assignedNames(tokens []string) []string {
	var names []string
	for _, tok := range tokens {
		if isAssignment(tok) {
			name, _, _ := strings.Cut(tok, "=")
			names = append(names, name)
		}
	}
	return names
}

// forget drops statically known values; later references stay unexpanded
// and are treated as unknown by the target checks.
func (s *shellAnalysisState) forget(names ...string) {
	for _, name := range names {
		delete(s.vars, name)
	}
}

// rebind drops the known value of every variable a builtin binds or removes
// at run time (read, printf -v, getopts, unset, export/declare, ...). The
// value is only known to the shell, so the earlier static value is stale.
func (s *shellAnalysisState) rebind(name string, inner []string) {
	operandName := func(tok string) string {
		tok, _, _ = strings.Cut(tok, "=")
		tok, _, _ = strings.Cut(tok, "[")
		return tok
	}
	switch name {
	case "read":
		s.forget("REPLY")
		for _, tok := range inner[1:] {
			s.forget(operandName(tok))
		}
	case "mapfile", "readarray":
		s.forget("MAPFILE")
		for _, tok := range inner[1:] {
			s.forget(operandName(tok))
		}
	case "getopts":
		s.forget("OPTARG", "OPTIND", "OPTERR")
		for _, tok := range inner[1:] {
			s.forget(operandName(tok))
		}
	case "printf":
		for i := 1; i < len(inner); i++ {
			if inner[i] == "-v" && i+1 < len(inner) {
				s.forget(operandName(inner[i+1]))
			} else if strings.HasPrefix(inner[i], "-v") && len(inner[i]) > 2 {
				s.forget(operandName(inner[i][2:]))
			}
		}
	case "unset", "export", "declare", "typeset", "local", "readonly", "let":
		for _, tok := range inner[1:] {
			if isShortFlagToken(tok) && strings.Contains(tok, "n") && name != "unset" && name != "let" {
				// declare -n makes a name an alias of another variable.
				clear(s.vars)
				return
			}
			s.forget(operandName(tok))
		}
	}
}

func (s *shellAnalysisState) assign(tokens []string) {
	for _, tok := range tokens {
		if !isAssignment(tok) {
			continue
		}
		name, value, _ := strings.Cut(tok, "=")
		s.vars[name] = expandEnvVars(value)
	}
}

func resolveInDirectory(path, cwd string) string {
	path = expandShellTokenPath(path)
	if !filepath.IsAbs(path) {
		path = cwd + string(filepath.Separator) + path
	}
	if resolved, err := resolvePathTarget(path); err == nil {
		return resolved
	}
	return path
}

func (s *shellAnalysisState) targetRisk(target, cwd string, known bool) RiskClass {
	target = expandShellTokenPath(target)
	if strings.ContainsAny(target, "$*?[]") || strings.Contains(target, dynamicSubstToken) {
		return Unknown
	}
	if (!known || s.uncertain) && !filepath.IsAbs(expandShellTokenPath(target)) {
		return Unknown
	}
	// Keep stdio aliases intact: Linux resolves /dev/stderr through procfs
	// to the runner's output pipe, which is not a destructive write target.
	abs := target
	if !filepath.IsAbs(abs) {
		abs = cwd + string(filepath.Separator) + abs
	}
	if isDirectBenignDevice(abs) {
		return LocalWrite
	}
	path := resolveInDirectory(target, cwd)
	return worstOf(ClassifyPathWrite(path), classifyResourceToken(path))
}

// isInputOutputRedirect reports whether tok is a redirection operator whose
// next token is its target.
func isInputOutputRedirect(tok string) bool {
	switch tok {
	case "<", "<<", "<<<", "<&", "<>":
		return true
	}
	return isRedirectToken(tok)
}

// directoryOperand returns the directory a cd/pushd stage moves to. Redirect
// operators with their targets (and the file descriptor digit before them) and
// the -L/-P/-e/-@ options are skipped. known is false when the destination
// cannot be determined (no pushd operand, stack rotation, other options, more
// than one operand).
func directoryOperand(name string, args []string) (path string, known bool) {
	var operands []string
	optionsDone := false
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if isInputOutputRedirect(tok) {
			i++
			continue
		}
		if isAllDigits(tok) && i+1 < len(args) && isInputOutputRedirect(args[i+1]) {
			continue
		}
		if !optionsDone {
			if tok == "--" {
				optionsDone = true
				continue
			}
			if isShortFlagToken(tok) {
				if strings.Trim(tok[1:], "LPe@") == "" {
					continue
				}
				return "", false
			}
			if strings.HasPrefix(tok, "--") || (strings.HasPrefix(tok, "+") && len(tok) > 1) {
				return "", false
			}
		}
		operands = append(operands, tok)
	}
	switch len(operands) {
	case 0:
		if name == "cd" {
			return os.Getenv("HOME"), true
		}
		return "", false
	case 1:
		return operands[0], true
	}
	return "", false
}

// skipWrapperArguments returns the index just past the options and numeric
// operands that the wrapper name takes after position from, mirroring how
// unwrapWrappers walks them, so a following wrapper such as env is seen.
func skipWrapperArguments(name string, tokens []string, from int) int {
	i := from
	for i < len(tokens) {
		t := tokens[i]
		switch {
		case t == "--":
			return i + 1
		case strings.HasPrefix(t, "-") && t != "-":
			if wrapperOptionTakesValue(name, t) && i+1 < len(tokens) {
				i += 2
				continue
			}
			i++
		case (name == "timeout" || name == "nice" || name == "ionice") && isNumericish(t):
			i++
		default:
			return i
		}
	}
	return i
}

// wrapperOptionTakesValue reports whether the wrapper's option consumes the
// following token as its value.
func wrapperOptionTakesValue(name, option string) bool {
	if argvComposers[name] {
		return xargsValueFlags[option]
	}
	switch name {
	case "watch":
		return option == "-n" || option == "--interval"
	case "strace":
		return hasAny([]string{"-e", "-p", "-o", "--output", "-s"}, option)
	case "timeout":
		return hasAny([]string{"-s", "--signal", "-k", "--kill-after"}, option)
	case "stdbuf":
		return hasAny([]string{"-i", "-o", "-e", "--input", "--output", "--error"}, option)
	}
	return false
}

func wrapperDirectory(tokens []string, cwd string) (string, bool) {
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if isAssignment(tok) {
			continue
		}
		name := commandName(tok)
		if !execWrappers[name] && !privilegedWrappers[name] {
			break
		}
		if name != "env" {
			i = skipWrapperArguments(name, tokens, i+1) - 1
			continue
		}
		for j := i + 1; j < len(tokens); j++ {
			tok = tokens[j]
			if tok == "--" {
				i = j
				break
			}
			if !strings.HasPrefix(tok, "-") && !isAssignment(tok) {
				i = j - 1
				break
			}
			var path string
			if tok == "-C" || tok == "--chdir" {
				if j+1 >= len(tokens) {
					return cwd, false
				}
				j++
				path = tokens[j]
			} else if strings.HasPrefix(tok, "--chdir=") {
				path = strings.TrimPrefix(tok, "--chdir=")
			} else if strings.HasPrefix(tok, "-C") {
				path = tok[2:]
			} else {
				if hasAny([]string{"-u", "--unset", "-S", "--split-string"}, tok) && j+1 < len(tokens) {
					j++
				}
				continue
			}
			if strings.ContainsAny(path, "$*?[]") {
				return cwd, false
			}
			cwd = resolveInDirectory(path, cwd)
			st, err := os.Stat(cwd)
			if err != nil || !st.IsDir() {
				return cwd, false
			}
		}
	}
	return cwd, true
}

var specialCommandNames = map[string]bool{
	"odek": true, "shutdown": true, "reboot": true, "halt": true, "poweroff": true,
	"init": true, "telinit": true, "source": true, ".": true,
	"docker": true, "docker-compose": true, "podman": true, "nerdctl": true,
	"direnv": true, "hugo": true, "aws": true, "gcloud": true, "az": true,
	"kubectl": true, "helm": true, "terraform": true, "gsutil": true,
}

func explicitUntrustedExecutable(path string) bool {
	if !strings.Contains(path, "/") {
		return false
	}
	if !filepath.IsAbs(path) {
		return true
	}
	if safeShellDirs[filepath.Dir(path)] {
		return false
	}
	resolved, err := exec.LookPath(filepath.Base(path))
	if err != nil {
		return true
	}
	a, errA := resolvePathTarget(path)
	b, errB := resolvePathTarget(resolved)
	return errA != nil || errB != nil || a != b
}
