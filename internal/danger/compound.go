package danger

import "strings"

// This file reads the shell grammar above the simple command: lists,
// pipelines, loops, conditionals, case, groups, subshells, functions and the
// test and arithmetic commands. It turns the token stream into a small tree
// that the analysis walks, so every simple command inside a compound is
// classified on its own instead of the whole construct failing closed on its
// first keyword.
//
// The parser never drops text. Anything it cannot pair (an unterminated
// construct, a stray keyword, a data region that contains operators a real
// one cannot) sets the bad flag, which the analysis reports as unknown, and
// parsing resumes so the commands that remain are still judged.

// maxCompoundDepth bounds how deeply compound commands may nest. A deeper
// program is not parsed at all; the analysis falls back to a flat reading and
// reports unknown.
const maxCompoundDepth = 32

type shNodeKind int

const (
	nodeGroup shNodeKind = iota
	nodeSubshell
	nodeIf
	nodeWhile
	nodeFor
	nodeCase
	nodeFunc
	nodeTest
	nodeArith
	nodeCoproc
)

// shList is a command list: items joined by ;, &&, || and &.
type shList struct{ items []shItem }

// shItem is one pipeline together with the operator that precedes it.
type shItem struct {
	op     string
	stages []shStage
}

// shStage is one pipeline stage: a simple command (words) or a compound.
type shStage struct {
	words []string
	comp  *shNode
}

type shArm struct {
	cond shList // if/elif condition
	pats []string
	body shList
	term string // case arm terminator: ;; ;& ;;&
}

// shNode is a compound command.
type shNode struct {
	kind  shNodeKind
	body  shList // group, subshell, loop body, coproc
	cond  shList // while/until condition
	until bool
	arms  []shArm  // if branches, case arms
	els   *shList  // if else branch
	name  string   // for/select variable, function name
	words []string // for/select word list, case word (one element), test words
	hasIn bool     // for/select with an explicit word list
	// header holds the (( )) header of an arithmetic for loop.
	header string
	arith  bool
	sel    bool
	fn     *shNode  // function body
	redirs []string // redirections applied to the whole construct
	size   int      // tokens the construct spans
}

// shProgram is a parsed command line.
type shProgram struct {
	list  shList
	bad   bool // some construct could not be paired
	deep  bool // nesting exceeded maxCompoundDepth
	async bool // contains coproc
}

type shParser struct {
	toks   []string
	pos    int
	depth  int
	bad    bool
	deep   bool
	async  bool
	frames [][]string // closers the enclosing constructs wait for
	// budget bounds the bytes re-tokenized from arithmetic bodies, so nested
	// bodies cannot make parsing quadratic.
	budget int
}

// literalMark prefixes a token that looks like a structural one but was
// written as a word (a quoted parenthesis); the parser reads it as an
// ordinary word and strips the mark again from the words it returns.
const literalMark = "\x00lit:"

func unmark(tok string) string { return strings.TrimPrefix(tok, literalMark) }

// parseShell parses a token stream produced by tokenizeMarked. ops reports
// which tokens are operators; nil means all of them are.
func parseShell(tokens []string, ops []bool) shProgram {
	p := &shParser{toks: append([]string(nil), tokens...)}
	for _, tok := range tokens {
		p.budget += 4 * len(tok)
	}
	p.budget += 1024
	if ops != nil {
		for i, tok := range p.toks {
			if !ops[i] && (tok == "(" || tok == ")" || isArithToken(tok) || operatorLookalikes[tok]) {
				p.toks[i] = literalMark + tok
			}
		}
	}
	list := p.parseList()
	for p.pos < len(p.toks) && !p.deep {
		// A closer nothing is waiting for: keep going so the commands after
		// it are still read.
		p.bad = true
		p.pos++
		more := p.parseList()
		list.items = append(list.items, more.items...)
	}
	if p.deep {
		return shProgram{list: flatList(p.toks), bad: true, deep: true}
	}
	return shProgram{list: list, bad: p.bad, async: p.async}
}

// flatList reads tokens as plain separated simple commands, without any
// compound structure.
func flatList(tokens []string) shList {
	var list shList
	ops := segmentOperators(tokens)
	for i, segment := range splitSegments(tokens) {
		item := shItem{op: ops[i]}
		for _, stage := range splitPipes(segment) {
			item.stages = append(item.stages, shStage{words: stage})
		}
		list.items = append(list.items, item)
	}
	return list
}

func (p *shParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *shParser) atEnd() bool { return p.pos >= len(p.toks) }

func (p *shParser) push(closers ...string) { p.frames = append(p.frames, closers) }
func (p *shParser) pop()                   { p.frames = p.frames[:len(p.frames)-1] }

// waitingFor reports whether any enclosing construct expects tok as a closer.
func (p *shParser) waitingFor(tok string) bool {
	for _, frame := range p.frames {
		for _, closer := range frame {
			if closer == tok {
				return true
			}
		}
	}
	return false
}

var strayClosers = map[string]bool{
	"then": true, "do": true, "done": true, "fi": true, "esac": true,
	"else": true, "elif": true, "}": true, ")": true,
	";;": true, ";&": true, ";;&": true,
}

func isListSeparator(tok string) bool {
	switch tok {
	case ";", "&&", "||", "&", ";;", ";&", ";;&":
		return true
	}
	return false
}

// parseList reads commands until end of input or a closer an enclosing
// construct waits for. The closers the caller itself expects must already be
// pushed.
func (p *shParser) parseList() shList {
	var list shList
	pending := ""
	for !p.deep {
		for !p.atEnd() {
			tok := p.peek()
			if !isListSeparator(tok) || p.waitingFor(tok) {
				break
			}
			if tok == ";" && (pending == "&&" || pending == "||") {
				p.pos++
				continue
			}
			if tok == ";;" || tok == ";&" || tok == ";;&" {
				// Case terminators only exist inside a case.
				p.bad = true
				tok = ";"
			}
			pending = tok
			p.pos++
		}
		if p.atEnd() {
			break
		}
		tok := p.peek()
		if p.waitingFor(tok) {
			break
		}
		if strayClosers[tok] {
			p.bad = true
			p.pos++
			continue
		}
		before := p.pos
		stages := p.parsePipeline()
		if p.pos == before {
			// No progress: a token nothing claims. Skip it rather than loop.
			p.bad = true
			p.pos++
			continue
		}
		if len(stages) == 1 && stages[0].comp == nil && len(stages[0].words) == 0 {
			continue
		}
		list.items = append(list.items, shItem{op: pending, stages: stages})
		pending = ""
		if next := p.peek(); !p.atEnd() && !isListSeparator(next) && !p.waitingFor(next) && !strayClosers[next] {
			// Something follows a finished compound command without a
			// separator (`done echo x`).
			p.bad = true
			pending = ";"
		}
	}
	return list
}

func (p *shParser) parsePipeline() []shStage {
	var stages []shStage
	for {
		stages = append(stages, p.parseStage())
		if tok := p.peek(); !p.atEnd() && (tok == "|" || tok == "|&") {
			p.pos++
			if p.atEnd() {
				stages = append(stages, shStage{})
				break
			}
			continue
		}
		break
	}
	return stages
}

// compoundStart reports whether tok opens a compound command.
func compoundStart(tok string) bool {
	switch tok {
	case "{", "(", "[[", "if", "for", "while", "until", "case", "select":
		return true
	}
	return isArithToken(tok)
}

// isArithToken matches the single token the tokenizer emits for an
// arithmetic command body, "((" … "))".
func isArithToken(tok string) bool {
	return len(tok) >= 4 && strings.HasPrefix(tok, "((") && strings.HasSuffix(tok, "))")
}

func (p *shParser) parseStage() shStage {
	if p.atEnd() {
		return shStage{}
	}
	tok := p.peek()
	switch {
	case tok == "|" || tok == "|&":
		return shStage{}
	case p.waitingFor(tok) || strayClosers[tok] || isListSeparator(tok):
		return shStage{}
	case tok == "!":
		p.pos++
		return p.parseStage()
	case tok == "time" && p.timeKeyword():
		p.pos++
		for strings.HasPrefix(p.peek(), "-") && !p.atEnd() {
			p.pos++
		}
		return p.parseStage()
	case tok == "coproc":
		return p.parseCoproc()
	case compoundStart(tok) || tok == "function":
		if n := p.parseCompound(); n != nil {
			return shStage{comp: n}
		}
	}
	return p.parseSimple()
}

// timeKeyword reports whether `time` at the current position is the keyword
// (it times a compound command or a negated one) rather than the wrapper
// command, which the simple-command analysis already unwraps.
func (p *shParser) timeKeyword() bool {
	j := p.pos + 1
	for j < len(p.toks) && strings.HasPrefix(p.toks[j], "-") {
		j++
	}
	return j < len(p.toks) && (compoundStart(p.toks[j]) || p.toks[j] == "!")
}

func (p *shParser) parseCoproc() shStage {
	start := p.pos
	p.pos++
	p.async = true
	if !p.atEnd() && !compoundStart(p.peek()) && isIdentifier(p.peek()) && p.pos+1 < len(p.toks) && compoundStart(p.toks[p.pos+1]) {
		p.pos++
	}
	var inner shStage
	if p.atEnd() || isListSeparator(p.peek()) || p.peek() == "|" {
		p.bad = true
		inner = shStage{}
	} else {
		inner = p.parseStage()
	}
	n := &shNode{kind: nodeCoproc, size: p.pos - start}
	n.body.items = []shItem{{stages: []shStage{inner}}}
	return shStage{comp: n}
}

// parseSimple reads one simple command: words up to a separator, pipe or the
// closing parenthesis of an enclosing subshell. Parentheses inside a
// command that are balanced (find \( … \)) are words.
func (p *shParser) parseSimple() shStage {
	start := p.pos
	var words []string
	parens := 0
	for !p.atEnd() {
		tok := p.peek()
		if isListSeparator(tok) || tok == "|" || tok == "|&" {
			break
		}
		if tok == "(" {
			parens++
		} else if tok == ")" {
			if parens == 0 && p.waitingFor(")") {
				break
			}
			if parens > 0 {
				parens--
			}
		}
		words = append(words, unmark(tok))
		p.pos++
	}
	if len(words) >= 3 && p.toks[start+1] == "(" && p.toks[start+2] == ")" && isFunctionName(words[0]) && !isAssignment(words[0]) {
		// name ( ) compound
		p.pos = start + 3
		if fn := p.parseFunction(words[0], start); fn != nil {
			return shStage{comp: fn}
		}
		return shStage{words: words[:3]}
	}
	if len(words) == 0 && p.pos == start && !p.atEnd() {
		// A lone closer or operator the caller did not claim.
		p.bad = true
		p.pos++
	}
	return shStage{words: words}
}

func isIdentifier(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isShellVarByte(s[i]) {
			return false
		}
	}
	return true
}

// isFunctionName accepts the words a function definition may be named with;
// reserved words and anything with shell syntax in it are not names.
func isFunctionName(s string) bool {
	if s == "" || strayClosers[s] || compoundStart(s) || isListSeparator(s) {
		return false
	}
	return !strings.ContainsAny(s, "$`(){}<>|&;=\"' \t")
}

// parseFunction reads the body after `name ( )` or after `function name`.
// It returns nil, without consuming the body, when none follows.
func (p *shParser) parseFunction(name string, start int) *shNode {
	p.skipSemicolons()
	if p.atEnd() || !compoundStart(p.peek()) {
		p.bad = true
		return nil
	}
	body := p.parseCompound()
	if body == nil {
		p.bad = true
		return nil
	}
	return &shNode{kind: nodeFunc, name: name, fn: body, size: p.pos - start}
}

// enter bounds nesting depth; leave undoes it.
func (p *shParser) enter() bool {
	p.depth++
	if p.depth > maxCompoundDepth {
		p.deep = true
		return false
	}
	return true
}

func (p *shParser) leave() { p.depth-- }

// parseCompound reads the compound command at the current position. It
// returns nil, with the position unchanged, when the construct is not
// genuine (the keyword is then read as an ordinary command word).
func (p *shParser) parseCompound() *shNode {
	start := p.pos
	tok := p.peek()
	if !p.enter() {
		return nil
	}
	defer p.leave()
	var n *shNode
	switch {
	case tok == "function":
		return p.parseFunctionKeyword()
	case tok == "{":
		p.pos++
		n = &shNode{kind: nodeGroup}
		p.push("}")
		n.body = p.parseList()
		p.pop()
		p.expect("}")
	case tok == "(":
		p.pos++
		n = &shNode{kind: nodeSubshell}
		p.push(")")
		n.body = p.parseList()
		p.pop()
		p.expectParen()
	case isArithToken(tok):
		if p.budget -= len(tok); p.budget < 0 {
			p.deep = true
			return nil
		}
		p.pos++
		n = &shNode{kind: nodeArith, words: tokenize(tok[2 : len(tok)-2])}
		for _, w := range n.words {
			if w == ";" || w == ";;" || w == ";&" || w == ";;&" {
				// A real arithmetic command has no command separators; the
				// text is commands behind an escaped bracket. Read them as
				// commands in place of the token.
				spliced := append([]string(nil), p.toks[:start]...)
				spliced = append(spliced, n.words...)
				p.toks = append(spliced, p.toks[start+1:]...)
				p.pos = start
				p.bad = true
				return nil
			}
		}
	case tok == "[[":
		n = p.parseTestCommand()
	case tok == "if":
		n = p.parseIf()
	case tok == "while" || tok == "until":
		n = p.parseWhile()
	case tok == "for" || tok == "select":
		n = p.parseFor()
	case tok == "case":
		n = p.parseCase()
	}
	if n == nil {
		p.pos = start
		return nil
	}
	n.redirs = p.parseRedirs()
	n.size = p.pos - start
	return n
}

func (p *shParser) parseFunctionKeyword() *shNode {
	start := p.pos
	p.pos++
	if p.atEnd() || !isFunctionName(p.peek()) {
		p.bad = true
		p.pos = start
		return nil
	}
	name := p.peek()
	p.pos++
	if p.peek() == "(" && p.pos+1 < len(p.toks) && p.toks[p.pos+1] == ")" {
		p.pos += 2
	}
	fn := p.parseFunction(name, start)
	if fn == nil {
		p.pos = start
	}
	return fn
}

// expect consumes closer, or records an unterminated construct.
func (p *shParser) expect(closer string) {
	if p.peek() == closer && !p.atEnd() {
		p.pos++
		return
	}
	p.bad = true
}

// expectParen consumes the closing parenthesis of a subshell.
func (p *shParser) expectParen() {
	if p.peek() == ")" && !p.atEnd() {
		p.pos++
		return
	}
	p.bad = true
}

func (p *shParser) skipSemicolons() {
	for !p.atEnd() && p.peek() == ";" {
		p.pos++
	}
}

func (p *shParser) parseIf() *shNode {
	n := &shNode{kind: nodeIf}
	p.pos++ // if
	for {
		arm := shArm{}
		p.push("then")
		arm.cond = p.parseList()
		p.pop()
		if len(arm.cond.items) == 0 {
			p.bad = true
		}
		if p.peek() == "then" && !p.atEnd() {
			p.pos++
		} else {
			p.bad = true
		}
		p.push("elif", "else", "fi")
		arm.body = p.parseList()
		p.pop()
		if len(arm.body.items) == 0 {
			p.bad = true
		}
		n.arms = append(n.arms, arm)
		switch {
		case p.atEnd():
			p.bad = true
			return n
		case p.peek() == "elif":
			p.pos++
			continue
		case p.peek() == "else":
			p.pos++
			p.push("fi")
			els := p.parseList()
			p.pop()
			if len(els.items) == 0 {
				p.bad = true
			}
			n.els = &els
			p.expect("fi")
			return n
		default:
			p.expect("fi")
			return n
		}
	}
}

func (p *shParser) parseWhile() *shNode {
	n := &shNode{kind: nodeWhile, until: p.peek() == "until"}
	p.pos++
	p.push("do")
	n.cond = p.parseList()
	p.pop()
	if len(n.cond.items) == 0 {
		p.bad = true
	}
	if p.peek() == "do" && !p.atEnd() {
		p.pos++
	} else {
		p.bad = true
	}
	p.push("done")
	n.body = p.parseList()
	p.pop()
	if len(n.body.items) == 0 {
		p.bad = true
	}
	p.expect("done")
	return n
}

// dataBreak reports whether tok is a token no word list or pattern list of a
// genuine for/case can contain.
func dataBreak(tok string) bool {
	switch tok {
	case "&&", "||", "&", "|", "|&", ";;", ";&", ";;&", "(", ")":
		return true
	}
	return false
}

func (p *shParser) parseFor() *shNode {
	n := &shNode{kind: nodeFor, sel: p.peek() == "select"}
	p.pos++
	if !p.atEnd() && isArithToken(p.peek()) && !n.sel {
		n.arith = true
		n.header = p.peek()
		p.pos++
	} else {
		if p.atEnd() || !isIdentifier(p.peek()) {
			p.bad = true
			return nil
		}
		n.name = p.peek()
		p.pos++
		if p.peek() == "in" && !p.atEnd() {
			n.hasIn = true
			p.pos++
			for !p.atEnd() && p.peek() != ";" {
				if dataBreak(p.peek()) {
					p.bad = true
					return nil
				}
				n.words = append(n.words, unmark(p.peek()))
				p.pos++
			}
		} else if !p.atEnd() && p.peek() != ";" {
			p.bad = true
			return nil
		}
	}
	p.skipSemicolons()
	p.push("done")
	if p.peek() == "do" && !p.atEnd() {
		p.pos++
	} else {
		p.bad = true
	}
	n.body = p.parseList()
	p.pop()
	if len(n.body.items) == 0 {
		p.bad = true
	}
	p.expect("done")
	return n
}

func (p *shParser) parseCase() *shNode {
	n := &shNode{kind: nodeCase}
	p.pos++
	if p.atEnd() || dataBreak(p.peek()) || isListSeparator(p.peek()) {
		p.bad = true
		return nil
	}
	n.words = []string{unmark(p.peek())}
	p.pos++
	if p.peek() != "in" || p.atEnd() {
		p.bad = true
		return nil
	}
	p.pos++
	for {
		p.skipSemicolons()
		if p.atEnd() {
			p.bad = true
			return n
		}
		if p.peek() == "esac" {
			p.pos++
			return n
		}
		if p.peek() == "(" {
			p.pos++
		}
		arm := shArm{}
		for {
			if p.atEnd() {
				p.bad = true
				return n
			}
			tok := p.peek()
			if tok == ")" {
				p.pos++
				break
			}
			if tok == "|" {
				p.pos++
				continue
			}
			if dataBreak(tok) || isListSeparator(tok) {
				// Not a pattern list: the keyword was not a case.
				if len(n.arms) == 0 {
					p.bad = true
					return nil
				}
				p.bad = true
				return n
			}
			arm.pats = append(arm.pats, unmark(tok))
			p.pos++
		}
		p.push(";;", ";&", ";;&", "esac")
		arm.body = p.parseList()
		p.pop()
		switch tok := p.peek(); {
		case p.atEnd():
			p.bad = true
			n.arms = append(n.arms, arm)
			return n
		case tok == ";;" || tok == ";&" || tok == ";;&":
			arm.term = tok
			p.pos++
		}
		n.arms = append(n.arms, arm)
	}
}

// parseTestCommand reads [[ … ]]. A test expression is data, so everything up
// to the closing ]] is kept as words. Command separators cannot appear in a
// real one: their presence means the bracket was not a keyword.
func (p *shParser) parseTestCommand() *shNode {
	n := &shNode{kind: nodeTest}
	p.pos++
	for !p.atEnd() {
		tok := p.peek()
		if tok == "]]" {
			p.pos++
			return n
		}
		if tok == ";" && len(n.words) > 0 && (n.words[len(n.words)-1] == "&&" || n.words[len(n.words)-1] == "||") {
			// A line break after && or || continues the expression.
			p.pos++
			continue
		}
		if tok == ";" || tok == "&" || tok == ";;" || tok == ";&" || tok == ";;&" {
			p.bad = true
			return nil
		}
		n.words = append(n.words, unmark(tok))
		p.pos++
	}
	p.bad = true
	return nil
}

var redirectOperators = map[string]bool{
	">": true, ">>": true, ">&": true, ">>&": true, "&>": true, "&>>": true, ">|": true,
	"<": true, "<<": true, "<<<": true, "<&": true, "<>": true,
}

// parseRedirs reads the redirections that follow a compound command.
func (p *shParser) parseRedirs() []string {
	var out []string
	for !p.atEnd() {
		tok := p.peek()
		if isAllDigits(tok) && p.pos+1 < len(p.toks) && redirectOperators[p.toks[p.pos+1]] {
			out = append(out, tok)
			p.pos++
			tok = p.peek()
		}
		if !redirectOperators[tok] {
			break
		}
		out = append(out, tok)
		p.pos++
		if p.atEnd() || isListSeparator(p.peek()) || p.peek() == "|" || p.peek() == "|&" || p.peek() == ")" {
			break
		}
		out = append(out, unmark(p.peek()))
		p.pos++
	}
	return out
}

// collectStages appends every simple-command stage of the list, descending
// into compound commands. Word lists, patterns and test expressions are data
// and are not included.
func (l shList) collectStages(out *[][]string) {
	for _, item := range l.items {
		for _, stage := range item.stages {
			if stage.comp != nil {
				stage.comp.collectStages(out)
			} else if len(stage.words) > 0 {
				*out = append(*out, stage.words)
			}
		}
	}
}

func (n *shNode) collectStages(out *[][]string) {
	n.body.collectStages(out)
	n.cond.collectStages(out)
	for _, arm := range n.arms {
		arm.cond.collectStages(out)
		arm.body.collectStages(out)
	}
	if n.els != nil {
		n.els.collectStages(out)
	}
	if n.fn != nil {
		n.fn.collectStages(out)
	}
	if len(n.redirs) > 0 {
		*out = append(*out, n.redirs)
	}
}

// commandStages returns every simple-command stage found in the tokens, with
// compound structure removed.
func commandStages(tokens []string, ops []bool) [][]string {
	var out [][]string
	parseShell(tokens, ops).list.collectStages(&out)
	return out
}

// containsJump reports whether the list can leave a loop or function early.
func (l shList) containsJump() bool {
	var stages [][]string
	l.collectStages(&stages)
	for _, stage := range stages {
		for _, tok := range stage {
			switch tok {
			case "break", "continue", "return", "exit":
				return true
			}
		}
	}
	return false
}

// Bounds on repeated analysis of loop bodies.
const (
	// maxLoopElements is the longest static for list analysed element by
	// element; a longer list binds its variable to the dynamic marker.
	maxLoopElements = 64
	// maxLoopPasses bounds the passes a loop body gets before the state it
	// changes is given up entirely.
	maxLoopPasses = 8
)

// pipeCtx describes where a command list runs: whether its stdin is a pipe
// (and which stages feed it) and whether it is a pipeline stage of its own
// (a subshell whose state changes do not reach the caller).
type pipeCtx struct {
	piped    bool
	upstream [][]string
	subshell bool
}

// stageUpstream returns the stages that feed stage i: those the enclosing
// context pipes in, then the earlier stages of this pipeline.
func stageUpstream(ctx pipeCtx, prepared [][]string, i int) [][]string {
	if len(ctx.upstream) == 0 {
		return prepared[:i]
	}
	out := append([][]string(nil), ctx.upstream...)
	return append(out, prepared[:i]...)
}

// chainState records what a `&&` chain changed, so the state is dropped when
// the chain ends: the changes only happened if every earlier operand did.
type chainState struct {
	vars map[string]bool
	cwd  bool
}

// record notes the variables and directory a compound command changed.
func (c *chainState) record(s *shellAnalysisState, before stateSnap) {
	for name, value := range s.vars {
		if old, ok := before.vars[name]; !ok || old != value {
			c.vars[name] = true
		}
	}
	if s.cwd != before.cwd || s.uncertain != before.uncertain {
		c.cwd = true
	}
}

// stateSnap is a copy of the analysis state a branch or iteration starts from.
type stateSnap struct {
	cwd       string
	uncertain bool
	vars      map[string]string
}

func (s *shellAnalysisState) snapshot() stateSnap {
	vars := make(map[string]string, len(s.vars))
	for name, value := range s.vars {
		vars[name] = value
	}
	return stateSnap{cwd: s.cwd, uncertain: s.uncertain, vars: vars}
}

func (s *shellAnalysisState) restore(snap stateSnap) {
	s.cwd, s.uncertain = snap.cwd, snap.uncertain
	s.vars = make(map[string]string, len(snap.vars))
	for name, value := range snap.vars {
		s.vars[name] = value
	}
}

func (a stateSnap) equal(b stateSnap) bool {
	if a.cwd != b.cwd || a.uncertain != b.uncertain || len(a.vars) != len(b.vars) {
		return false
	}
	for name, value := range a.vars {
		if other, ok := b.vars[name]; !ok || other != value {
			return false
		}
	}
	return true
}

// joinSnapshots is the state that holds whichever of the snapshots the shell
// ends in: only what every one of them agrees on stays known, and a
// directory that differs becomes unknown.
func joinSnapshots(base stateSnap, others ...stateSnap) stateSnap {
	out := stateSnap{cwd: base.cwd, uncertain: base.uncertain, vars: make(map[string]string, len(base.vars))}
	for name, value := range base.vars {
		out.vars[name] = value
	}
	for _, other := range others {
		if other.cwd != base.cwd || other.uncertain != base.uncertain {
			out.uncertain = true
		}
		for name, value := range out.vars {
			if v, ok := other.vars[name]; !ok || v != value {
				delete(out.vars, name)
			}
		}
	}
	return out
}

// staticElements returns the words a for loop iterates when they are all
// known at analysis time: an explicit list of at most maxLoopElements plain
// words, with no expansion, glob or substitution left in them.
func (n *shNode) staticElements(s *shellAnalysisState) ([]string, bool) {
	if !n.hasIn || n.sel {
		return nil, false
	}
	words := s.expand(n.words)
	if len(words) > maxLoopElements {
		return nil, false
	}
	for _, w := range words {
		if strings.ContainsAny(w, "$`*?[{}\\") || strings.Contains(w, dynamicSubstToken) || strings.Contains(w, braceOverflowToken) {
			return nil, false
		}
	}
	return words, true
}

// globElements returns the words of a for list that are plain words or glob
// patterns, with nothing left to expand at run time.
func (n *shNode) globElements(s *shellAnalysisState) ([]string, bool) {
	if !n.hasIn || n.sel {
		return nil, false
	}
	words := s.expand(n.words)
	if len(words) > maxLoopElements {
		return nil, false
	}
	globbed := false
	for _, w := range words {
		if strings.ContainsAny(w, "$`{}\\") || strings.Contains(w, dynamicSubstToken) || strings.Contains(w, braceOverflowToken) {
			return nil, false
		}
		if strings.ContainsAny(w, "*?[") {
			globbed = true
		}
	}
	return words, globbed
}

// functionCallAt returns the index of the command word of a stage when it
// names a function defined earlier in the same command line, or -1.
func functionCallAt(stage []string, funcs map[string]*shNode) int {
	if len(funcs) == 0 {
		return -1
	}
	k := 0
	for k < len(stage) && isAssignment(stage[k]) {
		k++
	}
	if k < len(stage) && funcs[stage[k]] != nil {
		return k
	}
	return -1
}

// redirectFreeArguments drops redirection operators, their targets and the
// descriptor digits before them from a command's operands.
func redirectFreeArguments(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case redirectOperators[tok]:
			i++
		case isAllDigits(tok) && i+1 < len(args) && redirectOperators[args[i+1]]:
		default:
			out = append(out, tok)
		}
	}
	return out
}

// variableNames lists the identifier-shaped runs in text.
func variableNames(text string) []string {
	var names []string
	for i := 0; i < len(text); {
		if !isShellVarByte(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isShellVarByte(text[j]) {
			j++
		}
		if text[i] < '0' || text[i] > '9' {
			names = append(names, text[i:j])
		}
		i = j
	}
	return names
}

var testUnaryOperators = "abcdefghknoprstuvwxzGLNORS"

var testBinaryOperators = map[string]bool{
	"==": true, "=": true, "!=": true, "=~": true, "<": true, ">": true,
	"-eq": true, "-ne": true, "-lt": true, "-le": true, "-gt": true, "-ge": true,
	"-nt": true, "-ot": true, "-ef": true,
}

// testShaped reports whether a clause of a [[ ]] or (( )) expression has the
// shape of a test: one word, a unary operator and its operand, or two
// operands around a binary operator.
func testShaped(clause []string) bool {
	switch len(clause) {
	case 1:
		return true
	case 2:
		return len(clause[0]) == 2 && clause[0][0] == '-' && strings.IndexByte(testUnaryOperators, clause[0][1]) >= 0
	case 3:
		return testBinaryOperators[clause[1]]
	}
	return false
}

// testClauseRunsCommand reports whether a clause that reads as an operand of
// a test also names a command the shell would run were the bracket escaped:
// a known command, or a lone path.
func testClauseRunsCommand(clause []string) bool {
	head := clause[0]
	if name := commandName(head); isKnownCommandName(name) || specialCommandNames[name] {
		return true
	}
	return len(clause) == 1 && strings.Contains(head, "/")
}
