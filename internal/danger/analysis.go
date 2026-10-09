package danger

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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
func Analyze(cmd string) Analysis {
	defer beginPathMemo()()
	return analyzeAtDepth(cmd, 0)
}

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
	// work is shared by an analysis and every nested payload analysis it
	// spawns, so recursion cannot multiply the per-command token bound.
	work *analysisWork
	// args are the positional parameters of the function call being
	// analysed, when they are statically known; "$@" and "$*" expand to them.
	args      []string
	argsKnown bool
}

// maxAnalysisTokens bounds the tokens one Analyze call examines across the
// command and every nested payload (substitutions, shell -c strings, eval).
// Each token costs filesystem resolution, so an unbounded count turns a
// 64 KiB command into seconds of work; an exceeded budget fails closed as
// Unknown. Real commands, including long scripts passed to a shell, use a
// small fraction of it.
const maxAnalysisTokens = 4096

type analysisWork struct{ tokens int }

// Bound static expansion independently of recursion: repeated assignments
// can otherwise double a value at each stage without nesting a command.
const maxStaticWordBytes = 64 << 10

// MaxCommandBytes is the longest command the classifier analyses. A longer
// command classifies Unknown before any normalization runs and
// ActionForCommand denies it regardless of policy: no legitimate tool call
// needs a single 64 KiB shell string, and every analysis phase is allowed to
// assume bounded input.
const MaxCommandBytes = 64 << 10

func analyzeAtDepth(cmd string, depth int) Analysis { return analyzeWithState(cmd, depth, nil) }

func analyzeWithState(cmd string, depth int, inherited *shellAnalysisState) Analysis {
	var result Analysis
	if len(cmd) > MaxCommandBytes {
		result.add(Unknown)
		return result
	}
	if isRawBlocked(cmd) {
		result.add(Blocked)
		return result
	}
	if depth > maxSubstDepth {
		result.add(Unknown)
		return result
	}
	main, subs := normalize(cmd)
	// Here-document bodies are consumed by normalize but still expand
	// variables when their delimiter is unquoted, so the raw text is scanned too.
	if referencesSensitiveEnv(main) || (strings.Contains(cmd, "<<") && referencesSensitiveEnv(cmd)) {
		result.add(SystemWrite)
	}
	if hasBareCarriageReturn(main) {
		// The tokenizer splits at a lone CR; a shell keeps it in the word.
		result.add(Unknown)
	}
	tokens, ops, unterminated := tokenizeMarked(main)
	if unterminated {
		// The shell would reject this line, but an open quote has swallowed
		// the rest of it into one word; whatever followed cannot be judged.
		result.add(Unknown)
	}
	work := &analysisWork{}
	if inherited != nil && inherited.work != nil {
		work = inherited.work
	}
	work.tokens += len(tokens)
	if work.tokens > maxAnalysisTokens {
		result.add(Unknown)
		return result
	}
	cwd, err := os.Getwd()
	state := shellAnalysisState{cwd: cwd, vars: make(map[string]string), uncertain: err != nil, written: make(map[string]bool), work: work}
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
	prog := parseShell(tokens, ops)
	if prog.bad {
		// A construct that cannot be paired (unterminated, a stray keyword,
		// an operator a real one cannot hold) is judged by the commands it
		// contains, but is itself unanalysable.
		result.add(Unknown)
	}
	if len(subs) > 0 && hasAny(tokens, "cd", "pushd", "popd") {
		result.add(Unknown)
	}
	// Conditional alternatives and background state cannot be carried as one
	// deterministic cwd/variable snapshot. Stateful stages below fail closed.
	ambiguous := hasAny(tokens, "||", "&") || prog.async
	// Mutations made behind `&&` only happen when every earlier operand
	// succeeded. Inside the chain they are carried (the rest of the chain only
	// runs once they happened); when the chain ends, the state they touched
	// is unknown.
	chain := &chainState{vars: make(map[string]bool)}
	// substExecutes marks a stage that executes the output of a command or
	// process substitution (eval "$(…)", bash <(…)).
	substExecutes := false
	// bailed is set when the repeated analysis of loop bodies and function
	// calls exhausts the shared work budget; nothing further is analysed.
	bailed := false
	charge := func(n int) bool {
		work.tokens += n
		if work.tokens > maxAnalysisTokens && !bailed {
			bailed = true
			result.add(Unknown)
		}
		return !bailed
	}
	endChain := func() {
		for name := range chain.vars {
			delete(state.vars, name)
			delete(chain.vars, name)
		}
		if chain.cwd {
			state.uncertain = true
			chain.cwd = false
		}
	}
	// volatile names the variables an unrolled loop binds or changes: their
	// value differs from one iteration to the next.
	volatile := make(map[string]bool)
	funcs := make(map[string]*shNode)
	var funcDefs []*shNode
	funcCalled := make(map[*shNode]bool)
	funcRunning := make(map[string]bool)
	var (
		runList     func(shList, pipeCtx)
		runNode     func(*shNode, pipeCtx)
		runStages   func([]shStage, bool, pipeCtx)
		runFunction func(string, []string, pipeCtx)
	)
	runItem := func(item shItem, ctx pipeCtx) {
		afterAnd := item.op == "&&"
		if !afterAnd {
			endChain()
		}
		runStages(item.stages, afterAnd, ctx)
	}
	runList = func(list shList, ctx pipeCtx) {
		outer := chain
		chain = &chainState{vars: make(map[string]bool)}
		for _, item := range list.items {
			if bailed {
				break
			}
			runItem(item, ctx)
		}
		endChain()
		chain = outer
	}
	runStages = func(stages []shStage, afterAnd bool, ctx pipeCtx) {
		prepared := make([][]string, 0, len(stages))
		for _, st := range stages {
			if st.comp != nil {
				prepared = append(prepared, []string{"cat"})
				continue
			}
			prepared = append(prepared, state.expand(st.words))
		}
		var pipeline []string
		var repos []*gitRepoCtx
		for i, stage := range prepared {
			if stages[i].comp != nil {
				if i > 0 {
					pipeline = append(pipeline, "|")
				}
				pipeline = append(pipeline, "cat")
				var before stateSnap
				if afterAnd {
					before = state.snapshot()
				}
				runNode(stages[i].comp, pipeCtx{piped: ctx.piped || i > 0, upstream: stageUpstream(ctx, prepared, i), subshell: len(stages) > 1})
				if afterAnd {
					chain.record(&state, before)
				}
				continue
			}
			piped := i > 0 || ctx.piped
			upstream := stageUpstream(ctx, prepared, i)
			if call := functionCallAt(stage, funcs); call >= 0 {
				args := redirectFreeArguments(stage[call+1:])
				runFunction(stage[call], args, pipeCtx{piped: piped, upstream: upstream})
				for _, arg := range args {
					result.add(classifyResourceToken(arg))
				}
				rewritten := append([]string(nil), stage[:call]...)
				rewritten = append(rewritten, ":")
				stage = append(rewritten, stage[call+1:]...)
				prepared[i] = stage
			}
			legacyStage := append([]string(nil), stage...)
			stageCwd, cwdKnown := wrapperDirectory(stage, state.cwd)
			payloadState := state
			payloadState.cwd = stageCwd
			payloadState.uncertain = state.uncertain || !cwdKnown
			unwrappedStage := unwrapWrappersFull(stage)
			inner, floor := unwrappedStage.inner, unwrappedStage.floor
			for _, payload := range unwrappedStage.payloads {
				result.merge(analyzeWithState(payload, depth+1, &payloadState))
			}
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
			pipeline = append(pipeline, markWordOperators(legacyStage)...)
			// Preserve findings from each stage before pipeline summaries can
			// replace them with a differently configured higher-ranked class.
			repo := newGitRepoCtx(stageCwd, cwdKnown && !state.uncertain, stage[:len(stage)-len(inner)], state.vars, state.written)
			repos = append(repos, repo)
			result.add(classifyStageIn(legacyStage, piped, repo))
			if floor != Safe {
				result.add(floor)
				if environmentRunsCode(stage[:len(stage)-len(inner)]) {
					result.add(CodeExecution)
				}
			}
			if len(inner) == 0 {
				if len(unwrappedStage.splits) > 0 && cwdKnown && !state.uncertain {
					// `env -S 'bash script'` leaves nothing behind the
					// wrapper, yet the split string is the command that runs.
					files, rewritten := stageLedgerFiles(stage, stageCwd, state.written)
					for _, path := range files {
						result.addFile(path)
					}
					for _, path := range rewritten {
						result.addRewritten(path)
					}
				}
				if len(stages) == 1 {
					if ambiguous {
						state.forget(assignedNames(stage)...)
					} else {
						state.assign(stage)
						if afterAnd {
							for _, assigned := range assignedNames(stage) {
								chain.vars[assigned] = true
							}
						}
					}
				}
				continue
			}
			name := commandName(inner[0])
			for _, assigned := range state.rebind(name, inner, len(stages) == 1 && !ambiguous) {
				if afterAnd {
					chain.vars[assigned] = true
				}
			}
			if secretNameOperand(name, inner[1:]) || stageTouchesCredentialFile(stage, inner, displayVerbs[name]) || indirectSensitiveRef(stage, state.vars) {
				result.add(SystemWrite)
			}
			if programOperandUnresolvable(name, inner) {
				// The interpreter runs a file whose path only exists at run
				// time, so no read licence can be checked against it.
				result.add(Unknown)
			}
			if isCodeExecution(name, inner, repo) || explicitUntrustedExecutable(inner[0]) || (piped && (pipedShells[name] || isStdinExecInterpreter(name) || embeddedShellInterpreters[name])) {
				result.add(CodeExecution)
			}
			if piped && dbClientStdinRunsShell(name, upstream) {
				result.add(CodeExecution)
			}
			if isNetworkEgress(name, inner) {
				result.add(NetworkEgress)
			}
			feed := stdinFeed{piped: piped}
			if feed.piped {
				_, feed.static = staticPipePayload(upstream)
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
			if i > 0 && stdinProgramStage(name, inner) && stagesDecodeContent(prepared[:i]) {
				// Decoded or decompressed bytes cannot be fingerprinted, so
				// no read licence can describe the program that runs.
				result.add(Unknown)
			}
			if cwdKnown && !state.uncertain {
				files, rewritten := stageLedgerFiles(stage, stageCwd, state.written)
				// An interpreter fed by a pipe executes what the upstream
				// readers emit, so their file operands are the program.
				if piped && stdinProgramStage(name, inner) {
					for _, producer := range upstream {
						f, r := readerFeedFiles(producer, stageCwd, state.written)
						files = append(files, f...)
						rewritten = append(rewritten, r...)
					}
				}
				for _, path := range files {
					result.addFile(path)
				}
				for _, path := range rewritten {
					result.addRewritten(path)
				}
			}
			if substFeedsProgram(name, inner) {
				substExecutes = true
			}
			for _, target := range semanticWriteTargets(name, inner) {
				if target == "-" {
					continue
				}
				result.add(state.targetRisk(target, stageCwd, cwdKnown))
			}
			for j, tok := range stage {
				if isRedirectToken(tok) && j+1 < len(stage) {
					if (tok == ">&" || tok == ">>&") && (isAllDigits(stage[j+1]) || stage[j+1] == "-") {
						continue
					}
					if !redirectWritesFile(stage, j) {
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
					chain.cwd = true
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
		result.add(classifyPipelineIn(pipeline, repos))
	}
	// scanData classifies the words of a data region (a for list, a case word
	// or pattern, a test expression): each is a resource token, never a command.
	scanData := func(words []string) {
		for _, w := range state.expand(words) {
			if w == "" {
				continue
			}
			if resource := classifyResourceToken(w); resource != Safe {
				result.add(resource)
			}
		}
	}
	// shadow judges a clause of a test or arithmetic expression as the command
	// the shell would run if the bracket were not a keyword (an escaped or
	// brace-built `[[` is only a command name). Clauses that read as operands
	// of a real expression are left alone.
	shadow := func(clause []string, expression bool) {
		if bailed {
			return
		}
		clause = state.expand(clause)
		k := 0
		for k < len(clause) && isAssignment(clause[k]) {
			k++
		}
		if k >= len(clause) {
			return
		}
		clause = clause[k:]
		if head := clause[0]; strings.Contains(head, "$") || strings.Contains(head, dynamicSubstToken) {
			return
		}
		if (expression || testShaped(clause)) && !testClauseRunsCommand(clause) {
			return
		}
		saved := result
		result = Analysis{}
		before := state.snapshot()
		runStages([]shStage{{words: clause}}, false, pipeCtx{})
		state.restore(before)
		inner := result
		result = saved
		result.merge(inner)
	}
	runClauses := func(expression bool, words []string, separators ...string) {
		var clause []string
		flush := func() {
			if len(clause) > 0 {
				shadow(clause, expression)
			}
			clause = nil
		}
		for _, w := range words {
			if slices.Contains(separators, w) {
				flush()
				continue
			}
			clause = append(clause, w)
		}
		flush()
	}
	// bindLoop runs a loop body until the state entering it is stable: values
	// the body changes are forgotten before the next pass, so a later
	// iteration cannot see a value the first one did not.
	bindLoop := func(size int, pass func()) {
		for passes := 0; ; passes++ {
			entry := state.snapshot()
			pass()
			next := joinSnapshots(entry, state.snapshot())
			if next.equal(entry) {
				state.restore(next)
				return
			}
			if passes+1 >= maxLoopPasses {
				// No convergence: forget everything and take a last pass.
				state.vars = make(map[string]string)
				state.uncertain = true
				pass()
				state.vars = make(map[string]string)
				state.uncertain = true
				return
			}
			state.restore(next)
			if !charge(size) {
				return
			}
		}
	}
	// bindLoopVariable gives a for/select variable the word it takes. The
	// binding is an assignment like any other, so a variable the shell reads
	// at run time (PATH, LD_PRELOAD, GIT_PAGER, …) is judged as such, and it
	// holds even when the command's other assignments are not tracked.
	bindLoopVariable := func(name, value string) {
		runStages([]shStage{{words: []string{name + "=" + value}}}, false, pipeCtx{})
		state.vars[name] = value
	}
	runNode = func(n *shNode, ctx pipeCtx) {
		if bailed {
			return
		}
		nested := pipeCtx{piped: ctx.piped, upstream: ctx.upstream}
		var scope stateSnap
		scoped := ctx.subshell || n.kind == nodeSubshell || n.kind == nodeCoproc
		if scoped {
			scope = state.snapshot()
		}
		switch n.kind {
		case nodeGroup, nodeSubshell, nodeCoproc:
			runList(n.body, nested)
		case nodeIf:
			runList(n.arms[0].cond, nested)
			condEnd := state.snapshot()
			var ends []stateSnap
			for k, arm := range n.arms {
				if k > 0 {
					state.restore(condEnd)
					runList(arm.cond, nested)
					condEnd = state.snapshot()
				}
				runList(arm.body, nested)
				ends = append(ends, state.snapshot())
			}
			if n.els != nil {
				state.restore(condEnd)
				runList(*n.els, nested)
				ends = append(ends, state.snapshot())
				// With an else branch one of the branches always runs.
				state.restore(joinSnapshots(ends[0], ends[1:]...))
			} else {
				state.restore(joinSnapshots(condEnd, ends...))
			}
		case nodeWhile:
			bindLoop(n.size, func() {
				runList(n.cond, nested)
				runList(n.body, nested)
			})
		case nodeFor:
			scanData(n.words)
			if n.arith {
				runClauses(true, tokenize(n.header[2:len(n.header)-2]), ";", "&&", "||", "|", "&", "(", ")")
			}
			elements, static := n.staticElements(&state)
			switch {
			case n.arith || n.name == "":
				bindLoop(n.size, func() { runList(n.body, nested) })
			case static && !n.body.containsJump():
				if len(elements) == 0 {
					base := state.snapshot()
					delete(state.vars, n.name)
					runList(n.body, nested)
					state.restore(joinSnapshots(base, state.snapshot()))
					break
				}
				volatile[n.name] = true
				for k, element := range elements {
					if k > 0 && !charge(n.size) {
						break
					}
					bindLoopVariable(n.name, element)
					start := state.snapshot()
					runList(n.body, nested)
					for name, value := range state.vars {
						if old, ok := start.vars[name]; !ok || old != value {
							volatile[name] = true
						}
					}
				}
			case static:
				bindLoop(n.size, func() {
					base := state.snapshot()
					var ends []stateSnap
					for _, element := range elements {
						state.restore(base)
						bindLoopVariable(n.name, element)
						runList(n.body, nested)
						ends = append(ends, state.snapshot())
					}
					state.restore(joinSnapshots(base, ends...))
				})
			default:
				// Words that only glob can still name files: judge the body
				// once per word with the variable bound to the pattern, so a
				// script the loop runs through a glob is gated like the glob.
				// A pattern bound as the value expands exactly like the same
				// glob written literally, so the per-pattern passes judge the
				// body completely; only a list the shell builds at run time
				// (substitution, variable, "$@") needs the dynamic marker.
				if patterns, ok := n.globElements(&state); ok {
					bindLoop(n.size, func() {
						for _, pattern := range patterns {
							if !charge(n.size) {
								return
							}
							bindLoopVariable(n.name, pattern)
							runList(n.body, nested)
						}
					})
				} else {
					bindLoop(n.size, func() {
						bindLoopVariable(n.name, dynamicSubstToken)
						runList(n.body, nested)
					})
				}
			}
			if ambiguous && n.name != "" {
				// The loop may not have run at all: its variable is unknown.
				delete(state.vars, n.name)
			}
		case nodeCase:
			scanData(n.words)
			entry := state.snapshot()
			var ends []stateSnap
			var previous stateSnap
			fell := false
			for _, arm := range n.arms {
				scanData(arm.pats)
				start := entry
				if fell {
					start = joinSnapshots(entry, previous)
				}
				state.restore(start)
				runList(arm.body, nested)
				previous = state.snapshot()
				ends = append(ends, previous)
				fell = arm.term == ";&" || arm.term == ";;&"
			}
			state.restore(joinSnapshots(entry, ends...))
		case nodeFunc:
			funcs[n.name] = n.fn
			funcDefs = append(funcDefs, n.fn)
		case nodeTest:
			scanData(n.words)
			exp := state.expand(n.words)
			for k := 0; k+1 < len(exp); k++ {
				if isRedirectToken(exp[k]) {
					if risk := state.targetRisk(exp[k+1], state.cwd, !state.uncertain); Rank(risk) >= Rank(SystemWrite) && risk != Unknown {
						result.add(risk)
					}
				}
			}
			runClauses(false, n.words, "&&", "||", "|", "|&", "(", ")", "!")
		case nodeArith:
			for _, w := range n.words {
				for _, name := range variableNames(w) {
					state.forget(name)
				}
			}
			runClauses(true, n.words, ";", "&&", "||", "|", "&", "(", ")")
		}
		if scoped {
			state.restore(scope)
		}
		if len(n.redirs) > 0 {
			runStages([]shStage{{words: append([]string{":"}, n.redirs...)}}, false, nested)
		}
	}
	runFunction = func(name string, args []string, ctx pipeCtx) {
		body := funcs[name]
		funcCalled[body] = true
		if funcRunning[name] {
			// A function that calls itself, directly or through another, has
			// no bounded analysis.
			result.add(Unknown)
			return
		}
		if !charge(body.size + 1) {
			return
		}
		funcRunning[name] = true
		before := state.snapshot()
		outerArgs, outerKnown := state.args, state.argsKnown
		state.args, state.argsKnown = args, true
		for k := 1; k <= 9; k++ {
			key := strconv.Itoa(k)
			if k <= len(args) {
				state.vars[key] = args[k-1]
			} else {
				delete(state.vars, key)
			}
		}
		runNode(body, pipeCtx{piped: ctx.piped, upstream: ctx.upstream})
		for k := 1; k <= 9; k++ {
			key := strconv.Itoa(k)
			if value, ok := before.vars[key]; ok {
				state.vars[key] = value
			} else {
				delete(state.vars, key)
			}
		}
		state.args, state.argsKnown = outerArgs, outerKnown
		state.restore(joinSnapshots(before, state.snapshot()))
		funcRunning[name] = false
	}
	runList(prog.list, pipeCtx{})
	// A function that is defined and never called is still judged: its body
	// runs whenever a later command line calls it. Its arguments are unknown.
	for k := 0; k < len(funcDefs); k++ {
		body := funcDefs[k]
		if funcCalled[body] {
			continue
		}
		funcCalled[body] = true
		before := state.snapshot()
		outerArgs, outerKnown := state.args, state.argsKnown
		state.args, state.argsKnown = nil, false
		for j := 1; j <= 9; j++ {
			delete(state.vars, strconv.Itoa(j))
		}
		runNode(body, pipeCtx{})
		state.args, state.argsKnown = outerArgs, outerKnown
		state.restore(before)
	}
	// Substitution bodies are judged against the state the command ends in.
	// A variable an unrolled loop rebinds on each iteration has no single
	// value there, so it is dropped and the body treats it as unknown.
	subState := state
	subState.vars = make(map[string]string, len(state.vars))
	for name, value := range state.vars {
		if !volatile[name] {
			subState.vars[name] = value
		}
	}
	for _, sub := range subs {
		result.merge(analyzeWithState(sub, depth+1, &subState))
		if substExecutes && substitutionDecodes(sub) {
			result.add(Unknown)
		}
		if substExecutes && !state.uncertain {
			files, rewritten := substitutionReaderFiles(sub, state.cwd, state.written)
			for _, path := range files {
				result.addFile(path)
			}
			for _, path := range rewritten {
				result.addRewritten(path)
			}
		}
	}
	if len(result.Effects) == 0 {
		result.add(Safe)
	}
	sort.Slice(result.Effects, func(i, j int) bool { return Rank(result.Effects[i]) > Rank(result.Effects[j]) })
	return result
}

// hasBareCarriageReturn reports whether text holds a carriage return outside
// quotes that is neither part of a CRLF line ending nor trailing.
func hasBareCarriageReturn(text string) bool {
	text = strings.TrimSpace(text)
	if strings.IndexByte(text, '\r') < 0 {
		return false
	}
	single, double := false, false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case single:
			single = c != '\''
		case c == '\\' && i+1 < len(text):
			i++
		case double:
			double = c != '"'
		case c == '\'':
			single = true
		case c == '"':
			double = true
		case c == '\r' && text[i+1] != '\n':
			return true
		}
	}
	return false
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
	out := make([]string, 0, len(tokens))
	// Whitespace (and any assigned IFS characters) splits an unquoted value
	// into several operands, which cannot be judged as one path. A glob in
	// the value expands exactly as the same glob written literally would, so
	// it is kept and judged as that spelling.
	separators := " \t\n\r"
	if ifs, ok := s.vars["IFS"]; ok {
		separators += ifs
	}
	for i := range tokens {
		token := tokens[i]
		if s.argsKnown && (token == "$@" || token == "$*" || token == "${@}" || token == "${*}") {
			out = append(out, s.args...)
			continue
		}
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
		out = append(out, token)
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
		case ";", "&&", "||", "&", ";;", ";&", ";;&":
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
// The declaring builtins (export, declare, typeset, local, readonly) record
// a literal NAME=value when bind is set, and return the names they bound.
func (s *shellAnalysisState) rebind(name string, inner []string, bind bool) (names []string) {
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
	case "shift", "set":
		// The positional parameters change meaning.
		for k := 1; k <= 9; k++ {
			s.forget(strconv.Itoa(k))
		}
		s.argsKnown = false
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
		bound := declarationBindings(name, inner)
		for _, tok := range inner[1:] {
			if isShortFlagToken(tok) && strings.Contains(tok, "n") && name != "unset" && name != "let" && name != "export" {
				// declare -n makes a name an alias of another variable.
				clear(s.vars)
				return nil
			}
			op := operandName(tok)
			if !bound.keep[op] {
				s.forget(op)
			}
		}
		if bind {
			for op, value := range bound.values {
				s.vars[op] = value
				names = append(names, op)
			}
		}
	}
	return names
}

// declarationBinding is the static outcome of an export/declare/readonly
// operand list: the literal NAME=value pairs that bind a known value, and the
// names whose existing value the builtin leaves alone.
type declarationBinding struct {
	values map[string]string
	keep   map[string]bool
}

// declarationBindings reads the operands of a variable-declaring builtin. A
// literal NAME=value records the value; a bare NAME keeps whatever value is
// known (`export S` does not change S). Attribute flags that transform or
// retype the value (-i, -l, -u, -c, -a, -A), values built by substitutions
// and array literals are not recorded, so those names are dropped.
func declarationBindings(name string, inner []string) declarationBinding {
	out := declarationBinding{values: map[string]string{}, keep: map[string]bool{}}
	transparent := "xrgpn"
	if name == "unset" || name == "let" {
		return out
	}
	plain := true
	for _, tok := range inner[1:] {
		if isShortFlagToken(tok) && strings.Trim(tok[1:], transparent) != "" {
			plain = false
		}
		if tok == "--" || strings.HasPrefix(tok, "+") {
			plain = false
		}
	}
	if !plain {
		return out
	}
	for i := 1; i < len(inner); i++ {
		tok := inner[i]
		if strings.HasPrefix(tok, "-") {
			continue
		}
		varName, value, assigned := strings.Cut(tok, "=")
		// A substitution glued to the value is split off into its own word
		// by normalization, leaving a truncated value behind.
		if assigned && i+1 < len(inner) && strings.Contains(inner[i+1], dynamicSubstToken) {
			continue
		}
		if !isValidVarName(varName) {
			continue
		}
		switch {
		case !assigned:
			if name != "local" {
				out.keep[varName] = true
			}
		case strings.Contains(value, dynamicSubstToken) || strings.HasPrefix(value, "("):
		default:
			out.values[varName] = expandEnvVars(value)
		}
	}
	return out
}

func isValidVarName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isShellVarByte(name[i]) {
			return false
		}
	}
	return true
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
	risk := worstOf(ClassifyPathWrite(path), classifyResourceToken(path))
	if credentialPathToken(path) {
		risk = worstOf(risk, SystemWrite)
	}
	return risk
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

func wrapperDirectory(tokens []string, cwd string) (string, bool) {
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if isAssignment(tok) {
			continue
		}
		name := commandName(tok)
		step, isWrapper := wrapperAt(tokens, i)
		if !isWrapper {
			break
		}
		if name != "env" {
			i = step.next - 1
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
