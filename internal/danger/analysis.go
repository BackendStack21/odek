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
	segments := splitSegments(tokens)
	if len(subs) > 0 && hasAny(tokens, "cd", "pushd", "popd") {
		result.add(Unknown)
	}
	// Conditional alternatives and background state cannot be carried as one
	// deterministic cwd/variable snapshot. Stateful stages below fail closed.
	ambiguous := hasAny(tokens, "||", "&")
	for _, segment := range segments {
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
					if payload := flagArg(inner, "-c"); payload != "" {
						result.merge(analyzeWithState(payload, depth+1, &payloadState))
						for j := range legacyStage {
							if legacyStage[j] == "-c" && j+1 < len(legacyStage) {
								legacyStage[j+1] = "echo"
								break
							}
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
				if len(stages) == 1 && !ambiguous {
					state.assign(stage)
				}
				continue
			}
			name := commandName(inner[0])
			if isCodeExecution(name, inner) || explicitUntrustedExecutable(inner[0]) || (i > 0 && (pipedShells[name] || isStdinExecInterpreter(name) || embeddedShellInterpreters[name])) {
				result.add(CodeExecution)
			}
			if isNetworkEgress(name, inner) {
				result.add(NetworkEgress)
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
				state.uncertain = ambiguous || name == "popd"
				path := os.Getenv("HOME")
				if len(inner) > 1 {
					path = inner[len(inner)-1]
				}
				if strings.ContainsAny(path, "$*?[]") || path == "-" {
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

func (s *shellAnalysisState) expand(tokens []string) []string {
	out := append([]string(nil), tokens...)
	for i, token := range out {
		// Shell-local values are substituted without executing expansions.
		for name, value := range s.vars {
			braced := "${" + name + "}"
			if len(value) > 0 && strings.Count(token, braced) > maxStaticWordBytes/len(value) {
				token = dynamicSubstToken
				break
			}
			token = strings.ReplaceAll(token, "${"+name+"}", value)
			for pos := 0; pos < len(token); {
				start := strings.Index(token[pos:], "$"+name)
				if start < 0 {
					break
				}
				start += pos
				end := start + 1 + len(name)
				if end < len(token) && isShellVarByte(token[end]) {
					pos = end
					continue
				}
				if len(token)-(end-start)+len(value) > maxStaticWordBytes {
					token = dynamicSubstToken
					break
				}
				token = token[:start] + value + token[end:]
				pos = start + len(value)
			}
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
	"kubectl": true, "helm": true, "terraform": true,
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
