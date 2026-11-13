package danger

import (
	"maps"
	"regexp"
	"strings"
)

// Option-carrying environment variables.
//
// Some tools read extra command-line options from an environment variable:
// GNU tar prepends $TAR_OPTIONS, Info-ZIP zip and unzip read $ZIPOPT/$ZIP and
// $UNZIPOPT/$UNZIP, Maven (3.9+) reads $MAVEN_ARGS as arguments placed before
// the command line's, and the JVM launchers behind Maven, Gradle, sbt and the
// like hand $MAVEN_OPTS, $GRADLE_OPTS, $JAVA_OPTS, … to the JVM they start,
// even for `mvn -v`.
//
// A run of a tool with such a variable in effect (assigned on the command
// itself, given to env, or bound by an earlier statement of the command line
// in any form: plain assignment, export, declare/typeset/local/readonly, set
// -a) is analysed again with the variable's words spliced into its argv where
// the tool reads them, right after the program name, and also appended, and
// the worse verdict is kept. Every option class the per-tool adapters know —
// exec (`--to-command`, `-TT`), delete (`--remove-files`, `-m`), path target
// (`-C /etc`, `-d /etc`), settings and POM selection (`-s`, `-f`) — is
// therefore judged exactly like the same option typed out, with no second
// list of dangerous options. A value that cannot be resolved statically, a
// binding the analysis cannot follow (a branch, read, a nameref, eval, a
// declaration whose name is computed), fails closed when the tool then runs.

// optionEnvTools maps an option-carrying variable to the tools that read it.
var optionEnvTools = map[string][]string{
	"TAR_OPTIONS": {"tar", "gtar"},
	"ZIPOPT":      {"zip"}, "ZIP": {"zip"},
	"UNZIPOPT": {"unzip"}, "UNZIP": {"unzip"},
	"MAVEN_ARGS": {"mvn", "mvnw"},
}

// optionEnvNames lists the keys of optionEnvTools in a fixed order, so the
// words of several variables are spliced deterministically.
var optionEnvNames = []string{"TAR_OPTIONS", "ZIPOPT", "ZIP", "UNZIPOPT", "UNZIP", "MAVEN_ARGS"}

// optionEnvState records, for one command line and every payload analysed
// inside it, which option-carrying variables a statement binds, and whether
// a declaration binds a variable whose name is only known at run time. A tool
// that later runs without a statically known value for such a variable fails
// closed.
type optionEnvState struct {
	bound   map[string]bool
	dynamic bool
}

func newOptionEnvState() *optionEnvState {
	return &optionEnvState{bound: make(map[string]bool)}
}

// posixSpecialBuiltins keep the assignments written in front of them after
// they return (POSIX sh, bash --posix, dash).
var posixSpecialBuiltins = fieldSet(": . break continue eval exec exit export readonly return set shift times trap unset")

// noteStage records the option-carrying variables one simple command binds:
// every word of an assignment-only command or of the command it runs, and
// the assignments in front of a special builtin, which persist. Assignments
// in front of any other command only reach that command. `unset` removes a
// variable, which leaves nothing for a tool to read.
func (o *optionEnvState) noteStage(stage, inner []string) {
	if o == nil {
		return
	}
	words := stage
	if len(inner) > 0 {
		switch name := commandName(inner[0]); {
		case name == "unset":
			return
		case !posixSpecialBuiltins[name]:
			words = inner
		}
	}
	for _, w := range words {
		for _, name := range optionEnvNames {
			if optionEnvWordBinds(w, name) {
				o.bound[name] = true
			}
		}
	}
}

// optionEnvWordBinds reports whether a word can bind the variable name: an
// assignment (NAME=, NAME+=, NAME[...]=), a bare NAME operand (export, read,
// printf -v, getopts), the target of a nameref (ref=NAME), or a
// ${NAME=word}/${NAME:=word} expansion. A name inside another word (a file
// called ARCHIVE.ZIP) or a plain $NAME reference binds nothing.
func optionEnvWordBinds(w, name string) bool {
	if w == name || w == "-v"+name || strings.HasSuffix(w, "="+name) {
		return true
	}
	if rest, ok := strings.CutPrefix(w, name); ok && rest != "" && strings.IndexByte("=+[", rest[0]) >= 0 {
		return true
	}
	for from := 0; ; {
		i := strings.Index(w[from:], "${"+name)
		if i < 0 {
			return false
		}
		rest := w[from+i+2+len(name):]
		if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":=") {
			return true
		}
		from += i + 2
	}
}

// declaresComputedName reports a builtin that binds a variable whose name is
// only known at run time: `declare "$n=value"`, `read "$n"`, `printf -v
// "$n"`, or a nameref (`declare -n r=$n`) to such a name.
func declaresComputedName(name string, inner []string) bool {
	spec, ok := bindingBuiltinSpecs[name]
	if !ok {
		return false
	}
	r := spec.parse(inner[1:])
	for _, o := range r.opts {
		// read -a NAME and printf -v NAME bind the variable they name.
		if o.has && ((name == "read" && o.is("-a")) || (name == "printf" && o.is("-v"))) && computedWord(o.value) {
			return true
		}
	}
	if name == "printf" {
		return false // its operands are the format and its arguments
	}
	nameref := (name == "declare" || name == "typeset" || name == "local") && r.has("-n")
	args := r.args()
	for i, op := range args {
		varName, value, _ := strings.Cut(op, "=")
		if computedWord(varName) && (varName != dynamicSubstToken || i == 0 || !strings.HasSuffix(args[i-1], "=")) {
			// A substitution glued to an assignment's value is split off
			// into its own word; that word is the value, not a name.
			return true
		}
		if nameref && computedWord(value) {
			return true
		}
	}
	return false
}

// bindingBuiltinSpecs are the option grammars of the builtins that bind the
// variables their operands name (and, for read -a and printf -v, the variable
// an option names), so option values are not read as names.
var bindingBuiltinSpecs = map[string]optSpec{
	"declare": {}, "typeset": {}, "local": {}, "readonly": {}, "export": {},
	"read":    {short: "adinNptu"},
	"mapfile": {short: "dnOsuCc"}, "readarray": {short: "dnOsuCc"},
	"printf": {short: "v"},
}

// computedWord reports a word whose text is only known at run time.
func computedWord(w string) bool {
	return strings.Contains(w, dynamicSubstToken) || strings.ContainsAny(w, "$`")
}

// withOptionAssignments returns vars extended by the option-carrying
// assignments a command runs with, which also reach every command it starts
// (a `bash -c` string, a wrapper's payload). vars itself is not modified.
func withOptionAssignments(vars map[string]string, assigns []string) map[string]string {
	var out map[string]string
	for _, a := range assigns {
		name, v, ok := strings.Cut(a, "=")
		if !ok || optionEnvTools[strings.ToUpper(name)] == nil {
			continue
		}
		if out == nil {
			out = maps.Clone(vars)
		}
		out[name] = v
	}
	if out == nil {
		return vars
	}
	return out
}

// readsOptionEnv reports whether tool reads the option-carrying variable.
func readsOptionEnv(upper, tool string) bool {
	return hasString(optionEnvTools[upper], tool)
}

// optionEnvAnalysis judges the option-carrying variables in effect for the
// command inner: the assignments it runs with (inline, which win) and the
// values earlier statements bound. A JVM option variable bound earlier is
// judged like one assigned inline. When inner is a tool that reads one of
// the variables, the invocation is analysed again with the variable's words
// spliced into its argv; payload is the state that analysis starts from.
func (s *shellAnalysisState) optionEnvAnalysis(inner, inline []string, payload shellAnalysisState, depth int) Analysis {
	var result Analysis
	if len(inner) == 0 || s.optionsApplied {
		return result
	}
	values := make(map[string]string)
	for name, v := range s.vars {
		upper := strings.ToUpper(name)
		if jvmOptionEnvNames[upper] {
			result.add(toolOptionEnvRisk(upper, v, inner))
		}
		if optionEnvTools[upper] != nil {
			values[upper] = v
		}
	}
	for _, a := range inline {
		if name, v, ok := strings.Cut(a, "="); ok && optionEnvTools[strings.ToUpper(name)] != nil {
			values[strings.ToUpper(name)] = v
		}
	}
	tool := commandName(inner[0])
	var words []string
	for _, upper := range optionEnvNames {
		if !readsOptionEnv(upper, tool) {
			continue
		}
		v, set := values[upper]
		switch {
		case !set:
			if s.options != nil && (s.options.bound[upper] || s.options.dynamic) {
				result.add(SystemWrite)
			}
		case optionValueUnresolved(v):
			result.add(SystemWrite)
		default:
			words = append(words, tokenize(v)...)
		}
	}
	if len(words) == 0 {
		return result
	}
	quote := func(ws []string) string {
		out := make([]string, len(ws))
		for i, w := range ws {
			out[i] = aliasQuoteWord(w)
		}
		return strings.Join(out, " ")
	}
	head, opts, rest := aliasQuoteWord(inner[0]), quote(words), quote(inner[1:])
	payload.optionsApplied = true
	for _, line := range []string{head + " " + opts + " " + rest, head + " " + rest + " " + opts} {
		p := payload
		result.merge(analyzeWithState(line, depth+1, &p))
	}
	return result
}

// jvmOptionEnvNames are variables whose value becomes JVM arguments.
var jvmOptionEnvNames = map[string]bool{
	"MAVEN_OPTS": true, "GRADLE_OPTS": true, "JAVA_OPTS": true, "SBT_OPTS": true,
	"ANT_OPTS": true, "CATALINA_OPTS": true, "JAVA_ARGS": true,
}

// optionValueUnresolved reports a value whose words are only known at run time.
func optionValueUnresolved(val string) bool {
	return strings.Contains(val, dynamicSubstToken) || strings.ContainsAny(val, "$`")
}

// toolOptionEnvRisk judges NAME=val when NAME carries options for a tool.
// inner is the command the assignment applies to (nil when unknown, as for
// `export`).
func toolOptionEnvRisk(upper, val string, inner []string) RiskClass {
	if jvmOptionEnvNames[upper] {
		if optionValueUnresolved(val) || !jvmOptionsPlain(val) {
			return SystemWrite
		}
		return Safe
	}
	tools, ok := optionEnvTools[upper]
	if !ok || strings.TrimSpace(val) == "" {
		return Safe
	}
	if optionValueUnresolved(val) {
		return SystemWrite
	}
	words := tokenize(val)
	tool := tools[0]
	base := classifyStage([]string{tool}, false)
	risk := Safe
	consider := func(tokens []string) {
		if got := classifyStage(tokens, false); Rank(got) > Rank(base) {
			risk = worstOf(risk, got)
		}
	}
	consider(append([]string{tool}, words...))
	if len(inner) > 0 && readsOptionEnv(upper, commandName(inner[0])) {
		consider(append(append([]string{inner[0]}, words...), inner[1:]...))
		consider(append(append([]string(nil), inner...), words...))
	}
	return risk
}

var (
	jvmPlainOption = regexp.MustCompile(`^(-X(mx|ms|ss|mn)[0-9]+[kKmMgG]?|-XX:[+-][A-Za-z0-9]+|-XX:[A-Za-z0-9]+=[0-9]+[kKmMgG]?|-server|-client|-D[A-Za-z0-9_.-]+(=[A-Za-z0-9_./:+,-]*)?)$`)
	// jvmCodeProperty matches system properties that load code or change
	// where it is found.
	jvmCodeProperty = regexp.MustCompile(`(?i)class|path|loader|agent|ext|security|preload|exec|script|home|lib`)
)

// jvmOptionsPlain reports whether every word of a JVM option string is a
// plain tuning option (heap sizes, simple flags, inert system properties).
// Anything else — agents, boot classpaths, argument files, error hooks,
// properties that name code — is not plain.
func jvmOptionsPlain(val string) bool {
	for _, w := range tokenize(val) {
		if !jvmPlainOption.MatchString(w) {
			return false
		}
		if strings.HasPrefix(w, "-D") {
			name, _, _ := strings.Cut(w[2:], "=")
			if jvmCodeProperty.MatchString(name) {
				return false
			}
		}
	}
	return true
}
