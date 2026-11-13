package danger

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Option-carrying environment variables.
//
// Some programs read extra command-line words from an environment variable:
// GNU tar prepends $TAR_OPTIONS, Info-ZIP zip and unzip read $ZIPOPT/$ZIP and
// $UNZIPOPT/$UNZIP, the Maven launcher adds $MAVEN_ARGS (Maven 3.9+) and the
// Maven wrapper $MAVEN_CONFIG to its arguments, and the launchers of Maven,
// Gradle, sbt, Ant and Tomcat hand $MAVEN_OPTS, $GRADLE_OPTS, $JAVA_OPTS, ...
// to the JVM they start, even for `mvn -v`. They also run the java binary under
// $JAVA_HOME, and the Maven wrapper and Ant run $JAVACMD.
//
// A variable matters only to the programs that read it (optionEnvVars; names
// match exactly, as the programs spell them), and only when such a program
// runs later in the same command line with the variable in its environment:
// assigned on that command (in front of it, or through env or sudo), or
// exported by an earlier statement (export, declare/typeset/local -x, an
// assignment after set -a), or a name odek's own environment already exports.
// A shell variable that is never exported does not reach the program. A
// variable the command line never assigns holds the inherited value, which is
// outside the analysis. Any other command keeps its verdict: a program the
// analysis cannot see into (a script, a local binary, a build runner) is
// already judged as running code of its own choosing, whatever it inherits.
//
// The value is judged the way its reader takes it:
//   - tar, zip and unzip options, and Maven arguments other than a few output
//     and mode flags, are spliced into the program's argv right after its name
//     (and, for tools that read them in another order, appended) and the
//     invocation is analysed again, so every option the per-tool adapters know
//     is judged exactly like the same option typed out;
//   - JVM options are plain when every word is a listed tuning option or a
//     system property that is not in jvmCodeProperties; anything else (agents,
//     class and module paths, argument files, error hooks, a main class)
//     classifies system_write, as JAVA_TOOL_OPTIONS does;
//   - a java binary (JAVA_HOME/bin/java, JAVACMD) that is relative, in the
//     working directory or in a temporary directory classifies code_execution.
//
// A value the analysis cannot follow fails closed when the reader runs: a
// value only known at run time, a binding the analysis cannot follow (read,
// a nameref, a sourced file, eval, a declaration whose name is computed, a
// value a nested payload changed), or a value over maxOptionEnvWords words or
// maxOptionEnvBytes bytes, which classifies unknown.

// optionEnvKind says how a variable's readers take its value.
type optionEnvKind int

const (
	// optionArgv: command-line options spliced into the reader's argv.
	optionArgv optionEnvKind = iota
	// optionToolArgs: Maven arguments, spliced unless every word is benign.
	optionToolArgs
	// optionJVM: options of the JVM the reader starts.
	optionJVM
	// optionJavaHome: the JDK whose bin/java the reader runs.
	optionJavaHome
	// optionJavaCmd: the java binary the reader runs.
	optionJavaCmd
)

// optionEnvVar is one option-carrying variable and the programs that read it.
type optionEnvVar struct {
	name    string
	kind    optionEnvKind
	readers []string
}

var (
	mavenLaunchers  = []string{"mvn", "mvnw", "mvnDebug"}
	gradleLaunchers = []string{"gradle", "gradlew"}
	javaLaunchers   = []string{"mvn", "mvnw", "mvnDebug", "gradle", "gradlew", "sbt", "ant", "catalina.sh"}
)

// optionEnvVars lists the option-carrying variables in a fixed order, so the
// words of several variables are spliced deterministically.
var optionEnvVars = []optionEnvVar{
	{"TAR_OPTIONS", optionArgv, []string{"tar", "gtar"}},
	{"ZIPOPT", optionArgv, []string{"zip"}},
	{"ZIP", optionArgv, []string{"zip"}},
	{"UNZIPOPT", optionArgv, []string{"unzip"}},
	{"UNZIP", optionArgv, []string{"unzip"}},
	{"MAVEN_ARGS", optionToolArgs, mavenLaunchers},
	{"MAVEN_CONFIG", optionToolArgs, []string{"mvnw"}},
	{"MAVEN_OPTS", optionJVM, mavenLaunchers},
	{"MAVEN_DEBUG_OPTS", optionJVM, mavenLaunchers},
	{"GRADLE_OPTS", optionJVM, gradleLaunchers},
	{"JAVA_OPTS", optionJVM, []string{"gradle", "gradlew", "sbt", "catalina.sh"}},
	{"SBT_OPTS", optionJVM, []string{"sbt"}},
	{"ANT_OPTS", optionJVM, []string{"ant"}},
	{"CATALINA_OPTS", optionJVM, []string{"catalina.sh"}},
	{"JAVA_HOME", optionJavaHome, javaLaunchers},
	{"JAVACMD", optionJavaCmd, []string{"mvnw", "ant"}},
}

// optionEnvNames is the set of the variables' names.
var optionEnvNames = func() map[string]bool {
	out := make(map[string]bool)
	for _, v := range optionEnvVars {
		out[v.name] = true
	}
	return out
}()

// optionEnvReadBy returns the variables the program tool reads, in table order.
func optionEnvReadBy(tool string) []optionEnvVar {
	var out []optionEnvVar
	for _, v := range optionEnvVars {
		if hasString(v.readers, tool) {
			out = append(out, v)
		}
	}
	return out
}

// optionEnvVarRead returns the variable name when tool reads it.
func optionEnvVarRead(name, tool string) (optionEnvVar, bool) {
	for _, v := range optionEnvVars {
		if v.name == name && hasString(v.readers, tool) {
			return v, true
		}
	}
	return optionEnvVar{}, false
}

const (
	// maxOptionEnvWords and maxOptionEnvBytes bound the value spliced into a
	// reader's argv. Real values hold a few words; a longer one classifies
	// unknown rather than being analysed again at a cost that grows with it.
	maxOptionEnvWords = 64
	maxOptionEnvBytes = 4 << 10
)

// optionValueWords returns the words of value as its reader splits them, or,
// for a value the analysis does not take apart, the verdict it gets: unknown
// over the size bound, system_write when it is only known at run time.
func optionValueWords(value string) ([]string, RiskClass) {
	if len(value) > maxOptionEnvBytes {
		return nil, Unknown
	}
	if optionValueUnresolved(value) {
		return nil, SystemWrite
	}
	words := tokenize(value)
	if len(words) > maxOptionEnvWords {
		return nil, Unknown
	}
	return words, Safe
}

// optionValueUnresolved reports a value whose words are only known at run time.
func optionValueUnresolved(val string) bool {
	return strings.Contains(val, dynamicSubstToken) || strings.ContainsAny(val, "$`")
}

// unresolvedRisk is the verdict of a variable whose value the analysis cannot
// determine when its reader runs.
func (v optionEnvVar) unresolvedRisk() RiskClass {
	if v.kind == optionJavaHome || v.kind == optionJavaCmd {
		return CodeExecution
	}
	return SystemWrite
}

// valueRisk judges the value of a variable whose words are not spliced into
// the reader's argv (JVM options and java binaries).
func (v optionEnvVar) valueRisk(value string) RiskClass {
	switch v.kind {
	case optionJVM:
		return jvmOptionsRisk(value, 0)
	case optionJavaHome:
		home := strings.TrimSpace(value)
		if home == "" {
			return Safe
		}
		if optionValueUnresolved(home) {
			return CodeExecution
		}
		return javaBinaryRisk(filepath.Join(expandShellTokenPath(home), "bin", "java"))
	case optionJavaCmd:
		cmd := strings.TrimSpace(value)
		if cmd == "" || !strings.ContainsAny(cmd, "/$`") && !strings.Contains(cmd, dynamicSubstToken) {
			return Safe // a bare name is looked up on PATH
		}
		if optionValueUnresolved(cmd) {
			return CodeExecution
		}
		return javaBinaryRisk(expandShellTokenPath(cmd))
	}
	return Safe
}

// javaBinaryRisk judges the java binary a launcher runs: one in the working
// directory, in a temporary directory or named by a relative path is code the
// command line can have put there.
func javaBinaryRisk(path string) RiskClass {
	if !filepath.IsAbs(path) {
		return CodeExecution
	}
	path = filepath.Clean(path)
	if cwd, err := os.Getwd(); err == nil && pathWithin(path, filepath.Clean(cwd)) {
		return CodeExecution
	}
	for _, dir := range []string{os.TempDir(), "/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", "/dev/shm"} {
		if pathWithin(path, filepath.Clean(dir)) {
			return CodeExecution
		}
	}
	return Safe
}

// toolArgsBenign reports whether every word of a Maven argument list is an
// output or mode flag that cannot change what the build loads or runs.
func toolArgsBenign(words []string) bool {
	for _, w := range words {
		if !mavenBenignArgs[w] && !mavenBenignArgPattern.MatchString(w) {
			return false
		}
	}
	return true
}

var (
	mavenBenignArgs = fieldSet("-q --quiet -B --batch-mode -e --errors -X --debug -ntp --no-transfer-progress " +
		"-o --offline -U --update-snapshots -nsu --no-snapshot-updates -C --strict-checksums -c --lax-checksums " +
		"-fae --fail-at-end -ff --fail-fast -fn --fail-never -V --show-version")
	mavenBenignArgPattern = regexp.MustCompile(`^(-T[0-9]+(\.[0-9]+)?C?|--threads=[0-9]+(\.[0-9]+)?C?|--color=(always|never|auto)|-Dstyle\.color=(always|never|auto))$`)
)

// optionEnvFloor judges the option-carrying assignments a command runs with
// (written in front of it or given to env or sudo) when the command reads
// them. It is the per-stage judgement used wherever a single command is
// classified; the full analysis also splices the words into the argv.
func optionEnvFloor(assignments, inner []string) RiskClass {
	if len(inner) == 0 {
		return Safe
	}
	tool := commandName(inner[0])
	risk := Safe
	for _, a := range assignments {
		name, val, ok := strings.Cut(a, "=")
		if !ok {
			continue
		}
		v, reads := optionEnvVarRead(name, tool)
		if !reads {
			continue
		}
		if v.kind != optionArgv && v.kind != optionToolArgs {
			risk = worstOf(risk, v.valueRisk(val))
			continue
		}
		words, r := optionValueWords(val)
		if r != Safe {
			risk = worstOf(risk, r)
			continue
		}
		if len(words) == 0 || (v.kind == optionToolArgs && toolArgsBenign(words)) {
			continue
		}
		base := classifyStage(inner, false)
		for _, tokens := range [][]string{
			append(append([]string{inner[0]}, words...), inner[1:]...),
			append(append([]string(nil), inner...), words...),
		} {
			if got := classifyStage(tokens, false); Rank(got) > Rank(base) {
				risk = worstOf(risk, got)
			}
		}
	}
	return risk
}

// jvmOptionsRisk judges a JVM option string: safe when every word is plain,
// system_write otherwise. depth bounds the nested option strings a property
// such as org.gradle.jvmargs carries.
func jvmOptionsRisk(value string, depth int) RiskClass {
	words, r := optionValueWords(value)
	if r != Safe {
		return r
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if jvmValueOptions[w] {
			// --add-opens java.base/java.lang=ALL-UNNAMED: the next word is
			// the option's value.
			if i+1 >= len(words) || !jvmModuleValue.MatchString(words[i+1]) {
				return SystemWrite
			}
			i++
			continue
		}
		if !jvmOptionPlain(w, depth) {
			return SystemWrite
		}
	}
	return Safe
}

// jvmOptionPlain reports whether one JVM option word is plain: a listed tuning
// option, or a system property that is not in jvmCodeProperties with a value
// of ordinary characters.
func jvmOptionPlain(w string, depth int) bool {
	prop, ok := strings.CutPrefix(w, "-D")
	if !ok {
		return jvmPlainOption.MatchString(w)
	}
	name, value, _ := strings.Cut(prop, "=")
	if name == "org.gradle.jvmargs" {
		// The JVM options of the Gradle daemon.
		return depth == 0 && jvmOptionsRisk(value, depth+1) == Safe
	}
	if jvmCodeProperties[name] {
		return false
	}
	for _, prefix := range jvmCodePropertyPrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return jvmPlainProperty.MatchString(w)
}

var (
	// jvmPlainOption lists the JVM options that only tune the VM: heap and
	// stack sizes, boolean and numeric -XX flags, assertions, preview
	// features, verbose output, class-data sharing mode, and module access
	// opened to code that is already loaded.
	jvmPlainOption = regexp.MustCompile(`^(-X(mx|ms|ss|mn)[0-9]+[kKmMgG]?` +
		`|-XX:[+-][A-Za-z0-9]+|-XX:[A-Za-z0-9]+=[0-9]+(\.[0-9]+)?[kKmMgG%]?` +
		`|-server|-client|-(ea|da|esa|dsa|enableassertions|disableassertions|enablesystemassertions|disablesystemassertions)(:[A-Za-z0-9_.]*(\.\.\.)?)?` +
		`|--enable-preview|-verbose(:(gc|class|module|jni))?|-Xshare:(auto|on|off)` +
		`|--add-(opens|exports|reads|modules)=[A-Za-z0-9_./=,-]+)$`)
	jvmPlainProperty = regexp.MustCompile(`^-D[A-Za-z0-9_.-]+(=[A-Za-z0-9_./:+,-]*)?$`)
	// jvmValueOptions are the plain options whose value is the next word.
	jvmValueOptions = fieldSet("--add-opens --add-exports --add-reads --add-modules")
	jvmModuleValue  = regexp.MustCompile(`^[A-Za-z0-9_./=,-]+$`)
	// jvmCodeProperties are the system properties that load code into the
	// JVM or the build tool it runs, or point either at configuration that
	// names code: extension, library, boot and module paths, the system class
	// loader, the security manager and its policy, a remote codebase, the home
	// directory the build tools read their settings, plugins and init scripts
	// from, Maven core extensions and its native console library, and the
	// Gradle and sbt directories that hold JVMs, init scripts and plugins.
	jvmCodeProperties = fieldSet("java.ext.dirs java.endorsed.dirs java.library.path java.class.path " +
		"java.system.class.loader java.security.manager java.security.policy java.security.properties " +
		"java.rmi.server.codebase sun.boot.class.path sun.boot.library.path " +
		"jdk.module.path jdk.module.upgrade.path jdk.module.main user.home " +
		"maven.ext.class.path library.jansi.path " +
		"org.gradle.java.home gradle.user.home sbt.global.base sbt.boot.directory sbt.boot.properties")
	jvmCodePropertyPrefixes = []string{"jdk.module.patch"}
)

// optionEnvState records, for one command line and every payload analysed
// inside it, which option-carrying variables its statements bind or export.
// It is shared by the analysis of the command line and all nested payloads,
// whose own variable maps are copies: a binding made by a payload (eval, an
// alias body) is recorded here with the scope of the analysis that made it,
// so a reader in another analysis knows its own copy of the value is stale.
type optionEnvState struct {
	nextScope int
	// binders lists, per variable, the scopes of the analyses whose
	// statements bound it; scope -1 marks a binding no analysis can follow
	// (the target of a nameref).
	binders  map[string][]int
	exported map[string]bool
	// allexport is set by `set -a`: later assignments are exported.
	allexport bool
	// dynamic is set by a binding whose name is only known at run time and
	// by a sourced file: any variable may be bound and exported.
	dynamic bool
}

// maxOptionEnvBinders bounds the scopes recorded per variable; past it the
// variable is treated as bound where no analysis can follow it.
const maxOptionEnvBinders = 16

func newOptionEnvState() *optionEnvState {
	return &optionEnvState{binders: make(map[string][]int), exported: make(map[string]bool)}
}

// newScope returns the scope of an analysis starting under parent: its own id
// followed by those of every analysis it runs inside.
func (o *optionEnvState) newScope(parent []int) []int {
	o.nextScope++
	out := make([]int, 0, len(parent)+1)
	out = append(out, parent...)
	return append(out, o.nextScope)
}

// bind records that the analysis with the given scope binds name.
func (o *optionEnvState) bind(name string, scope []int) {
	if !optionEnvNames[name] {
		return
	}
	id := -1
	if len(scope) > 0 {
		id = scope[len(scope)-1]
	}
	if list := o.binders[name]; !hasInt(list, id) {
		if len(list) >= maxOptionEnvBinders {
			id = -1
		}
		o.binders[name] = append(list, id)
	}
	if o.allexport {
		o.exported[name] = true
	}
}

// stale reports whether a binding of name was made where the analysis with
// the given scope cannot see it: by an analysis other than itself and the
// ones it runs inside, or by no analysis at all.
func (o *optionEnvState) stale(name string, scope []int) bool {
	for _, id := range o.binders[name] {
		if !hasInt(scope, id) {
			return true
		}
	}
	return false
}

func hasInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// posixSpecialBuiltins keep the assignments written in front of them after
// they return (POSIX sh, bash --posix, dash).
var posixSpecialBuiltins = fieldSet(": . break continue eval exec exit export readonly return set shift times trap unset")

// noteOptionBindings records the option-carrying variables one simple command
// binds or exports: the words of an assignment-only command, the assignments
// in front of a special builtin (which persist), the operands of the binding
// builtins (export, declare, typeset, local, readonly, read, mapfile,
// readarray, printf -v, getopts), `set -a`, and a sourced file or a
// computed name, after which any variable may be bound. Assignments in front
// of any other command only reach that command.
func (s *shellAnalysisState) noteOptionBindings(stage, inner []string) {
	o := s.options
	if o == nil {
		return
	}
	if len(inner) == 0 {
		for _, w := range stage {
			if isAssignment(w) {
				name, _, _ := strings.Cut(w, "=")
				o.bind(name, s.optScope)
			}
		}
		return
	}
	name := commandName(inner[0])
	if posixSpecialBuiltins[name] {
		for _, w := range stage[:len(stage)-len(inner)] {
			if isAssignment(w) {
				n, _, _ := strings.Cut(w, "=")
				o.bind(n, s.optScope)
				o.exported[n] = o.exported[n] || optionEnvNames[n]
			}
		}
	}
	if declaresComputedName(name, inner) {
		o.dynamic = true
	}
	args := inner[1:]
	switch name {
	case "source", ".":
		// The sourced file can bind and export anything.
		o.dynamic = true
	case "set":
		if setsAllexport(args) {
			o.allexport = true
		}
	case "export", "declare", "typeset", "local", "readonly":
		s.noteDeclarationBindings(name, args)
	case "read", "mapfile", "readarray", "printf":
		r := bindingBuiltinSpecs[name].parse(args)
		for _, opt := range r.opts {
			if opt.has && ((name == "read" && opt.is("-a")) || (name == "printf" && opt.is("-v"))) {
				o.bind(operandVarName(opt.value), s.optScope)
			}
		}
		if name == "printf" {
			break
		}
		for _, op := range r.args() {
			o.bind(operandVarName(op), s.optScope)
		}
	case "getopts":
		if len(args) >= 2 {
			o.bind(operandVarName(args[1]), s.optScope)
		}
	}
}

// noteDeclarationBindings records the operands of export, declare, typeset,
// local and readonly: NAME=value and NAME+=value bind, -x (and export itself)
// exports, and a nameref (-n) makes its target bound where no analysis can
// follow it.
func (s *shellAnalysisState) noteDeclarationBindings(name string, args []string) {
	o := s.options
	exports, nameref := name == "export", false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			break
		}
		if a[0] == '-' {
			exports = exports || strings.ContainsRune(a[1:], 'x')
			nameref = nameref || strings.ContainsRune(a[1:], 'n')
		}
	}
	if name == "export" && nameref {
		// export -n removes the export attribute; the assignment still binds.
		exports, nameref = false, false
	}
	for _, op := range args[i:] {
		varName, value, assigned := strings.Cut(op, "=")
		varName = strings.TrimSuffix(varName, "+")
		if bracket := strings.IndexByte(varName, '['); bracket >= 0 {
			varName = varName[:bracket]
		}
		if nameref {
			if !assigned {
				o.dynamic = true // the nameref's target is set later
				continue
			}
			if computedWord(value) {
				o.dynamic = true
				continue
			}
			o.bind(value, []int{-1})
			o.exported[value] = o.exported[value] || optionEnvNames[value]
			continue
		}
		if assigned {
			o.bind(varName, s.optScope)
		}
		if exports && optionEnvNames[varName] {
			o.exported[varName] = true
		}
	}
}

// setsAllexport reports whether set's arguments turn on allexport (-a, or -o
// allexport), before the positional parameters start.
func setsAllexport(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			return false
		}
		if a == "-o" {
			if i+1 < len(args) && args[i+1] == "allexport" {
				return true
			}
			i++
			continue
		}
		if strings.ContainsRune(a[1:], 'a') {
			return true
		}
	}
	return false
}

// operandVarName returns the variable an operand names: NAME, NAME[sub] or
// NAME=value.
func operandVarName(tok string) string {
	tok, _, _ = strings.Cut(tok, "=")
	tok, _, _ = strings.Cut(tok, "[")
	return strings.TrimSuffix(tok, "+")
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

// withOptionAssignments returns vars and env extended by the option-carrying
// assignments a command runs with, which also reach every command it starts
// (a `bash -c` string, a wrapper's payload). Neither input is modified.
func withOptionAssignments(vars map[string]string, env map[string]bool, assigns []string) (map[string]string, map[string]bool) {
	outVars, outEnv := vars, env
	cloned := false
	for _, a := range assigns {
		name, v, ok := strings.Cut(a, "=")
		if !ok || !optionEnvNames[name] {
			continue
		}
		if !cloned {
			cloned = true
			outVars = maps.Clone(vars)
			if outVars == nil {
				outVars = make(map[string]string)
			}
			outEnv = maps.Clone(env)
			if outEnv == nil {
				outEnv = make(map[string]bool)
			}
		}
		outVars[name] = v
		outEnv[name] = true
	}
	return outVars, outEnv
}

// optionValueState says what the analysis knows of a variable when a reader
// runs.
type optionValueState int

const (
	optionAbsent optionValueState = iota
	optionKnown
	optionUnresolved
)

// optionEnvValue returns the value of name a reader started by the command
// with the given inline assignments sees.
func (s *shellAnalysisState) optionEnvValue(name string, inline map[string]string) (string, optionValueState) {
	if v, ok := inline[name]; ok {
		return v, optionKnown
	}
	o := s.options
	_, inherited := os.LookupEnv(name)
	if !o.exported[name] && !s.optEnv[name] && !o.dynamic && !inherited {
		return "", optionAbsent
	}
	if o.dynamic {
		return "", optionUnresolved
	}
	if len(o.binders[name]) == 0 && !s.optEnv[name] {
		// Never assigned by this command line: the inherited value.
		return "", optionAbsent
	}
	if o.stale(name, s.optScope) {
		return "", optionUnresolved
	}
	v, ok := s.vars[name]
	if !ok {
		return "", optionUnresolved
	}
	return v, optionKnown
}

// optionEnvAnalysis judges the option-carrying variables the command inner
// reads: the assignments it runs with (inline, which win) and the values in
// its environment. Words a reader takes as command-line options are spliced
// into its argv and the invocation is analysed again; payload is the state
// that analysis starts from.
func (s *shellAnalysisState) optionEnvAnalysis(inner, inline []string, payload shellAnalysisState, depth int) Analysis {
	var result Analysis
	if len(inner) == 0 || s.optionsApplied || s.options == nil {
		return result
	}
	readers := optionEnvReadBy(commandName(inner[0]))
	if len(readers) == 0 {
		return result
	}
	inlineValues := make(map[string]string)
	for _, a := range inline {
		if name, v, ok := strings.Cut(a, "="); ok && optionEnvNames[name] {
			inlineValues[name] = v
		}
	}
	var words []string
	for _, v := range readers {
		value, state := s.optionEnvValue(v.name, inlineValues)
		switch state {
		case optionAbsent:
			continue
		case optionUnresolved:
			result.add(v.unresolvedRisk())
			continue
		}
		if v.kind != optionArgv && v.kind != optionToolArgs {
			result.add(v.valueRisk(value))
			continue
		}
		w, risk := optionValueWords(value)
		if risk != Safe {
			result.add(risk)
			continue
		}
		if v.kind == optionToolArgs && toolArgsBenign(w) {
			continue
		}
		words = append(words, w...)
	}
	if len(words) == 0 {
		return result
	}
	if len(words) > maxOptionEnvWords {
		result.add(Unknown)
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
