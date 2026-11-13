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
// Gradle, sbt, Ant, Tomcat and the JVM languages hand $MAVEN_OPTS,
// $GRADLE_OPTS, $JAVA_OPTS, ... to the JVM they start, even for `mvn -v`. They
// also run the java binary under $JAVA_HOME or named by $JAVACMD, and the
// JDK's own tools are, on macOS, /usr/bin stubs that pick the JDK from
// $JAVA_HOME.
//
// A variable matters only to the programs that read it (optionEnvReaders;
// names match exactly, as the programs spell them), and only when such a
// program runs later in the same command line with the variable in its
// environment: assigned on that command (in front of it, or through env or
// sudo), or exported by an earlier statement (export, declare/typeset/local
// -x, an assignment after set -a), or a name odek's own environment already
// exports. A shell running a launcher script (`sh mvnw`, `bash ./gradlew`) is
// that launcher. A shell variable that is never exported does not reach the
// program. A variable the command line never assigns holds the inherited
// value, which is outside the analysis. Any other command keeps its verdict.
// Nothing takes a variable out of the judgement again: unset, export -n and
// declare +x between the assignment and the reader are not honoured, since a
// run-time word can turn each into a statement that leaves the variable
// exported (`unset $1 NAME` with -f, `export -fn NAME`).
//
// The value is judged the way its reader takes it:
//   - tar, zip and unzip options, and Maven arguments unless every word is
//     one of a few output and mode flags, are spliced into the program's argv
//     right after its name (and, for tools that read them in another order,
//     appended) and the invocation is analysed again, so every option the
//     per-tool adapters know is judged exactly like the same option typed out;
//   - JVM options are plain when every word is a listed tuning option or a
//     system property that is not in jvmCodeProperties; anything else (agents,
//     class and module paths, argument files, error hooks, a main class)
//     classifies system_write, as JAVA_TOOL_OPTIONS does;
//   - a java binary (JAVA_HOME/bin/java, JAVACMD) that is relative, in a
//     temporary directory, in the directory the reader or odek runs in, or
//     written by the command line itself classifies code_execution; the
//     directories JDKs are installed into are otherwise exempt.
//
// A value the analysis cannot follow fails closed when the reader runs: a
// value only known at run time, a binding the analysis cannot follow (read,
// a nameref, a sourced file, eval, a declaration whose name is computed, a
// value a nested payload changed), or a value over maxOptionEnvWords words or
// maxOptionEnvBytes bytes, which classifies unknown, as does a command line
// that splices more than maxOptionSpliceWords words in all.

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

// optionEnvVar is one option-carrying variable and how its readers take it.
type optionEnvVar struct {
	name string
	kind optionEnvKind
}

// optionEnvVars lists the option-carrying variables in a fixed order, so the
// words of several variables are spliced deterministically.
var optionEnvVars = []optionEnvVar{
	{"TAR_OPTIONS", optionArgv},
	{"ZIPOPT", optionArgv},
	{"ZIP", optionArgv},
	{"UNZIPOPT", optionArgv},
	{"UNZIP", optionArgv},
	{"MAVEN_ARGS", optionToolArgs},
	{"MAVEN_CONFIG", optionToolArgs},
	{"MAVEN_OPTS", optionJVM},
	{"MAVEN_DEBUG_OPTS", optionJVM},
	{"GRADLE_OPTS", optionJVM},
	{"JAVA_OPTS", optionJVM},
	{"SBT_OPTS", optionJVM},
	{"ANT_OPTS", optionJVM},
	{"CATALINA_OPTS", optionJVM},
	{"JAVA_HOME", optionJavaHome},
	{"JAVACMD", optionJavaCmd},
}

var (
	// jvmScriptVars are what a JVM launcher script whose variables are not
	// verified one by one is taken to read: the JVM options and both ways of
	// naming the java binary.
	jvmScriptVars = []string{"JAVA_OPTS", "JAVA_HOME", "JAVACMD"}
	// mavenVars are read by the Maven launchers; they run an inherited
	// $JAVACMD (the launcher scripts set it themselves in current releases,
	// which is not relied on) and take the JVM options from $MAVEN_OPTS only.
	mavenVars = []string{"MAVEN_ARGS", "MAVEN_OPTS", "MAVEN_DEBUG_OPTS", "JAVA_HOME", "JAVACMD"}
	// jdkToolVars are read by the JDK's own tools: they are native launchers,
	// but on macOS /usr/bin/java and its siblings are stubs that run the tool
	// of the JDK under $JAVA_HOME.
	jdkToolVars = []string{"JAVA_HOME"}
)

// optionEnvReaders is the one table of the programs that read option-carrying
// variables and the variables each reads. Every program that starts a JVM is
// listed: the Gradle start scripts and catalina.sh set the java command
// themselves and the Maven and Ant scripts take their own *_OPTS rather than
// $JAVA_OPTS; any other launcher script reads jvmScriptVars.
var optionEnvReaders = func() map[string][]string {
	out := map[string][]string{
		"tar": {"TAR_OPTIONS"}, "gtar": {"TAR_OPTIONS"},
		"zip":   {"ZIPOPT", "ZIP"},
		"unzip": {"UNZIPOPT", "UNZIP"},
		"mvn":   mavenVars, "mvnDebug": mavenVars,
		"mvnw":    append(append([]string(nil), mavenVars...), "MAVEN_CONFIG"),
		"gradle":  {"GRADLE_OPTS", "JAVA_OPTS", "JAVA_HOME"},
		"gradlew": {"GRADLE_OPTS", "JAVA_OPTS", "JAVA_HOME"},
		"sbt":     append([]string{"SBT_OPTS"}, jvmScriptVars...),
		"ant":     {"ANT_OPTS", "JAVA_HOME", "JAVACMD"},
		// catalina.sh runs $JRE_HOME/bin/java or $JAVA_HOME/bin/java.
		"catalina.sh": {"CATALINA_OPTS", "JAVA_OPTS", "JAVA_HOME"},
	}
	for _, name := range strings.Fields("kotlinc kotlinc-jvm kotlin scala scalac scala-cli amm groovy groovyc groovysh " +
		"lein clojure clj jruby jirb jython jbang grails spring cs coursier sbtn mvnd") {
		out[name] = jvmScriptVars
	}
	for _, name := range strings.Fields("java javac jar jarsigner javadoc javap jdeps jlink jmod jpackage jshell jdb jcmd " +
		"jconsole jinfo jmap jps jstack jstat jstatd jhsdb jfr keytool serialver rmiregistry jwebserver jrunscript " +
		"jdeprscan jimage native2ascii") {
		out[name] = jdkToolVars
	}
	return out
}()

// optionEnvNames is the set of the variables' names.
var optionEnvNames = func() map[string]bool {
	out := make(map[string]bool)
	for _, v := range optionEnvVars {
		out[v.name] = true
	}
	return out
}()

// optionSpliceNames is the set of the variables whose words are spliced into
// their reader's argv.
var optionSpliceNames = func() map[string]bool {
	out := make(map[string]bool)
	for _, v := range optionEnvVars {
		if v.kind == optionArgv || v.kind == optionToolArgs {
			out[v.name] = true
		}
	}
	return out
}()

// optionEnvReadBy returns the variables the program tool reads, in table order.
func optionEnvReadBy(tool string) []optionEnvVar {
	reads := optionEnvReaders[tool]
	if len(reads) == 0 {
		return nil
	}
	var out []optionEnvVar
	for _, v := range optionEnvVars {
		if hasString(reads, v.name) {
			out = append(out, v)
		}
	}
	return out
}

// optionEnvVarRead returns the variable name when tool reads it.
func optionEnvVarRead(name, tool string) (optionEnvVar, bool) {
	if !hasString(optionEnvReaders[tool], name) {
		return optionEnvVar{}, false
	}
	for _, v := range optionEnvVars {
		if v.name == name {
			return v, true
		}
	}
	return optionEnvVar{}, false
}

// optionReaderArgv returns the argv of the program that reads the variables
// when inner runs: inner itself, or, for a shell running a script file (`sh
// mvnw -v`, `bash -e ./gradlew build`), the script and its arguments, since
// the script is the launcher. An inline script (`sh -c`) or one read from
// standard input names no launcher.
func optionReaderArgv(inner []string) []string {
	if len(inner) == 0 || !pipedShells[commandName(inner[0])] {
		return inner
	}
	if i := shellScriptOperandIndex(inner); i > 0 {
		return inner[i:]
	}
	return inner
}

// shellScriptOperandIndex returns the index of the script file a shell
// invocation runs, or -1 when it runs an inline (-c) script, reads its program
// from standard input (-s), or has no operand. tokens[0] is the shell; the
// option grammar is shellInlineScriptIndex's.
func shellScriptOperandIndex(tokens []string) int {
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "--":
			if i+1 < len(tokens) {
				return i + 1
			}
			return -1
		case isRedirectToken(t):
			i++ // skip the redirect target
		case strings.HasPrefix(t, "--"):
			if t == "--rcfile" || t == "--init-file" {
				i++
			}
		case len(t) > 1 && (t[0] == '-' || t[0] == '+'):
			for _, r := range t[1:] {
				switch r {
				case 'c', 's':
					if t[0] == '-' {
						return -1
					}
				case 'o', 'O':
					i++ // option name follows as its own token
				}
			}
		default:
			return i
		}
	}
	return -1
}

const (
	// maxOptionEnvWords and maxOptionEnvBytes bound the value spliced into a
	// reader's argv. Real values hold a few words; a longer one classifies
	// unknown rather than being analysed again at a cost that grows with it.
	maxOptionEnvWords = 64
	maxOptionEnvBytes = 4 << 10
	// maxOptionSpliceWords bounds the words one Analyze call splices over all
	// its commands and nested payloads. Each splice analyses the reader
	// again, so without it a command of a few hundred short stages, each
	// within the per-value bound, costs hundreds of analyses. Past it nothing
	// more is spliced and the command classifies unknown. A real command line
	// runs a few readers with a few option words each.
	maxOptionSpliceWords = 4 * maxOptionEnvWords
)

// optionSpliceWords counts the words the NAME=value words of a command hold
// for the variables whose words are spliced into the argv of a reader the
// command names. A value over the per-value bound is not spliced and counts
// nothing.
func optionSpliceWords(stage []string) int {
	total := 0
	for _, tok := range stage {
		name, value, ok := strings.Cut(tok, "=")
		if !ok || !optionSpliceNames[name] || len(value) > maxOptionEnvBytes {
			continue
		}
		n := len(strings.Fields(value))
		if n == 0 || n > maxOptionEnvWords {
			continue
		}
		for _, other := range stage {
			if hasString(optionEnvReaders[commandName(other)], name) {
				total += n
				break
			}
		}
	}
	return total
}

// withoutOptionSpliceValues returns stage with the value of every spliced
// option variable replaced by one only known at run time, which its reader
// fails closed on without analysing anything again.
func withoutOptionSpliceValues(stage []string) []string {
	out := make([]string, len(stage))
	for i, tok := range stage {
		if name, _, ok := strings.Cut(tok, "="); ok && optionSpliceNames[name] {
			tok = name + "=" + dynamicSubstToken
		}
		out[i] = tok
	}
	return out
}

// optionValueWords returns the words of value as its reader splits them, or,
// for a value the analysis does not take apart, the verdict it gets: unknown
// over the size bound, system_write when it is only known at run time.
func optionValueWords(value string) ([]string, RiskClass) {
	if len(value) > maxOptionEnvBytes {
		return nil, Unknown
	}
	if computedWord(value) {
		return nil, SystemWrite
	}
	words := tokenize(value)
	if len(words) > maxOptionEnvWords {
		return nil, Unknown
	}
	return words, Safe
}

// unresolvedRisk is the verdict of a variable whose value the analysis cannot
// determine when its reader runs.
func (v optionEnvVar) unresolvedRisk() RiskClass {
	if v.kind == optionJavaHome || v.kind == optionJavaCmd {
		return CodeExecution
	}
	return SystemWrite
}

// javaPathCtx is where a java binary variable is judged: the directory its
// reader runs in (known is false when the analysis cannot tell), the
// assignments written on the reader's own command and the paths the command
// line wrote before the reader runs.
type javaPathCtx struct {
	cwd     string
	known   bool
	inline  []string
	written map[string]bool
	// homeRebound: the command line binds, reads or unsets HOME, or may, so
	// ~ and $HOME no longer name the home directory odek inherited.
	homeRebound bool
}

// processJavaPathCtx is the context of a command judged on its own: the
// directory odek runs in.
func processJavaPathCtx(inline []string) javaPathCtx {
	cwd, err := memoGetwd()
	return javaPathCtx{cwd: cwd, known: err == nil, inline: inline}
}

// moved reports whether the reader runs in a directory other than the one
// odek runs in, or one the analysis cannot tell.
func (c javaPathCtx) moved() bool {
	process, err := memoGetwd()
	if !c.known || err != nil {
		return true
	}
	for _, a := range bothSpellings(c.cwd) {
		for _, b := range bothSpellings(process) {
			if a == b {
				return false
			}
		}
	}
	return true
}

// resolve expands a leading tilde and the environment variables of a path
// value, as the shell does before it assigns. A reference the inherited
// environment does not answer stays as written: a variable assigned on the
// same command, and the working-directory forms ($PWD, $OLDPWD, ~+, ~-) once
// an earlier cd moved the shell.
func (c javaPathCtx) resolve(value string) string {
	names := []string{}
	for _, a := range c.inline {
		if name, _, ok := strings.Cut(a, "="); ok && name != "" {
			names = append(names, strings.TrimSuffix(name, "+"))
		}
	}
	if c.homeRebound {
		names = append(names, "HOME")
	}
	if c.moved() {
		if strings.HasPrefix(value, "~+") || strings.HasPrefix(value, "~-") {
			return value
		}
		names = append(names, "PWD", "OLDPWD")
	}
	for _, name := range names {
		if strings.Contains(value, "$"+name) || strings.Contains(value, "${"+name) {
			return value
		}
		// A leading tilde is $HOME.
		if name == "HOME" && strings.HasPrefix(value, "~") {
			return value
		}
	}
	return expandEnvVars(expandTilde(value))
}

// homeBindingWord matches a word that names HOME where a statement binds,
// reads into or unsets a variable: the bare name (`read HOME`, `unset HOME`,
// `for HOME in`, a nameref target), an assignment or append to it, also
// inside the text of a payload, and the ${HOME:=word} expansion that assigns.
// Names that only end in HOME (JAVA_HOME, XDG_CONFIG_HOME) and plain
// references ($HOME, ${HOME}) do not match.
var homeBindingWord = regexp.MustCompile(`(^|[^A-Za-z0-9_${])HOME($|[^A-Za-z0-9_/])|\$\{HOME:?=`)

// noteHomeBindings records whether any of the words rebinds HOME.
func (o *optionEnvState) noteHomeBindings(words []string) {
	if o == nil || o.homeRebound {
		return
	}
	for _, w := range words {
		if strings.Contains(w, "HOME") && homeBindingWord.MatchString(w) {
			o.homeRebound = true
			return
		}
	}
}

// valueRisk judges the value of a variable whose words are not spliced into
// the reader's argv (JVM options and java binaries).
func (v optionEnvVar) valueRisk(value string, ctx javaPathCtx) RiskClass {
	switch v.kind {
	case optionJVM:
		return jvmOptionsRisk(value, 0)
	case optionJavaHome:
		home := ctx.resolve(strings.TrimSpace(value))
		switch {
		case home == "":
			return Safe
		case computedWord(home):
			return CodeExecution
		}
		return javaBinaryRisk(filepath.Join(home, "bin", "java"), ctx)
	case optionJavaCmd:
		cmd := ctx.resolve(strings.TrimSpace(value))
		switch {
		case cmd == "":
			return Safe
		case computedWord(cmd):
			return CodeExecution
		case !strings.Contains(cmd, "/"):
			return Safe // a bare name is looked up on PATH
		}
		return javaBinaryRisk(cmd, ctx)
	}
	return Safe
}

// javaBinaryRisk judges the java binary a launcher runs: one named by a
// relative path, in a temporary directory, in the directory the launcher runs
// in or the one odek runs in, or anywhere when the launcher's directory is
// not known, is code the command line can have put there. The directories
// JDKs are installed into are not the project's files: the system ones
// wherever the launcher runs, the per-user ones while the launcher runs where
// odek does (so the home directory as the workspace does not make them
// project files), and neither when the command line itself wrote there.
func javaBinaryRisk(path string, ctx javaPathCtx) RiskClass {
	if !filepath.IsAbs(path) {
		return CodeExecution
	}
	// A directory reached through a symlink is the same directory: the path
	// and the working directories are compared as written and as resolved.
	paths := bothSpellings(path)
	within := func(dirs []string) bool {
		for _, p := range paths {
			for _, dir := range dirs {
				if dir == string(filepath.Separator) || pathWithin(p, dir) {
					return true
				}
			}
		}
		return false
	}
	if within([]string{filepath.Clean(os.TempDir()), "/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", "/dev/shm"}) {
		return CodeExecution
	}
	planted := false
	for w := range ctx.written {
		planted = planted || within([]string{filepath.Clean(w)})
	}
	system, user := jdkInstallDirs()
	if !planted && (within(system) || (!ctx.moved() && within(user))) {
		return Safe
	}
	var cwds []string
	if ctx.known {
		cwds = bothSpellings(ctx.cwd)
	}
	if process, err := memoGetwd(); err == nil {
		cwds = append(cwds, bothSpellings(process)...)
	}
	if !ctx.known || planted || within(cwds) {
		return CodeExecution
	}
	return Safe
}

// bothSpellings returns the cleaned path and, when it differs, the path with
// its symlinks resolved.
func bothSpellings(path string) []string {
	path = filepath.Clean(path)
	out := []string{path}
	if resolved, err := resolvePathTarget(path); err == nil && filepath.IsAbs(resolved) {
		if resolved = filepath.Clean(resolved); resolved != path {
			out = append(out, resolved)
		}
	}
	return out
}

// jdkInstallDirs lists the directories JDKs are installed into: the system
// locations, and the per-user directories of the JDK managers (SDKMAN, asdf,
// jenv, mise, jabba) and of the JDKs an IDE downloads.
func jdkInstallDirs() (system, user []string) {
	system = []string{"/usr/lib/jvm", "/usr/lib64/jvm", "/usr/java", "/usr/local/java", "/opt/java",
		"/Library/Java/JavaVirtualMachines", "/System/Library/Java", "/opt/homebrew/opt", "/usr/local/opt"}
	for _, home := range currentHomeDirs() {
		for _, rel := range []string{".sdkman/candidates/java", ".asdf/installs/java", ".jenv/versions",
			".local/share/mise/installs/java", ".jabba/jdk", "Library/Java/JavaVirtualMachines"} {
			user = append(user, filepath.Join(home, rel))
		}
	}
	return system, user
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
	inner = optionReaderArgv(inner)
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
			risk = worstOf(risk, v.valueRisk(val, processJavaPathCtx(assignments)))
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
	// loader, a remote codebase, the JDK and the home directory the build
	// tools read their settings, plugins and init scripts from, Maven's
	// installation, core extensions, local repository, master-password file
	// and native console library, and the Gradle and sbt directories and
	// repository lists that hold JVMs, init scripts, plugins and the
	// artifacts a build resolves.
	jvmCodeProperties = fieldSet("java.ext.dirs java.endorsed.dirs java.library.path java.class.path " +
		"java.system.class.loader java.home java.rmi.server.codebase sun.boot.class.path sun.boot.library.path " +
		"jdk.module.path jdk.module.upgrade.path jdk.module.main user.home " +
		"maven.ext.class.path maven.home maven.repo.local settings.security library.jansi.path " +
		"org.gradle.java.home org.gradle.java.installations.paths gradle.user.home " +
		"sbt.global.base sbt.boot.directory sbt.boot.properties sbt.ivy.home sbt.repository.config " +
		"sbt.override.build.repos")
	// jvmCodePropertyPrefixes: module patches, and the security manager, its
	// policy, the security properties file and the other java.security
	// settings (providers, login configuration).
	jvmCodePropertyPrefixes = []string{"jdk.module.patch", "java.security."}
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
	// tails holds, per variable, the text NAME+=value appended since its last
	// plain assignment. When the earlier value is unknown the variable holds
	// a run-time value ending in the tail, and the tail is judged as the
	// value too: with the variable unset, the append is exactly the
	// assignment of it.
	tails map[string]string
	// values holds, per variable, every literal value a statement of the
	// command line or of a payload assigned to it, up to maxOptionEnvValues;
	// dropped names the variables with more. A reader that runs when the
	// analysis no longer knows the value (after unset, read, a payload that
	// rebinds it) is judged with each of them: a statement that seems to
	// remove or replace a value never lowers the verdict the value had.
	values  map[string][]string
	dropped map[string]bool
	// homeRebound is set once a word of the command line or of a payload
	// names HOME where a variable is bound (see homeBindingWord).
	homeRebound bool
}

// maxOptionEnvValues bounds the values recorded per variable; a variable
// assigned more often classifies unknown where its value is not known.
const maxOptionEnvValues = 8

// maxOptionEnvBinders bounds the scopes recorded per variable; past it the
// variable is treated as bound where no analysis can follow it.
const maxOptionEnvBinders = 16

func newOptionEnvState() *optionEnvState {
	return &optionEnvState{binders: make(map[string][]int), exported: make(map[string]bool), tails: make(map[string]string),
		values: make(map[string][]string), dropped: make(map[string]bool)}
}

// noteValue records a literal value assigned to name.
func (o *optionEnvState) noteValue(name, value string) {
	if !optionEnvNames[name] || value == "" || hasString(o.values[name], value) {
		return
	}
	if len(o.values[name]) >= maxOptionEnvValues {
		o.dropped[name] = true
		return
	}
	o.values[name] = append(o.values[name], value)
}

// noteAppend records the text a NAME+=value operand appends (appended) or the
// plain assignment that replaces the value and the earlier appends.
func (o *optionEnvState) noteAppend(name, value string, appended bool) {
	if !optionEnvNames[name] {
		return
	}
	if !appended {
		delete(o.tails, name)
		return
	}
	tail := o.tails[name] + value
	if len(tail) > maxOptionEnvBytes {
		// Kept just over the bound, so the tail still classifies unknown.
		tail = tail[:maxOptionEnvBytes+1]
	}
	o.tails[name] = tail
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
				name, value, _ := strings.Cut(w, "=")
				o.bind(name, s.optScope)
				o.noteAppend(name, "", false)
				o.noteValue(name, value)
			}
		}
		return
	}
	name := commandName(inner[0])
	if posixSpecialBuiltins[name] {
		for _, w := range stage[:len(stage)-len(inner)] {
			if isAssignment(w) {
				n, value, _ := strings.Cut(w, "=")
				o.bind(n, s.optScope)
				o.noteAppend(n, "", false)
				o.noteValue(n, value)
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
	if name == "export" {
		// export has no nameref option, and its -n (remove the export
		// attribute) is not honoured: the operands count as exported.
		nameref = false
	}
	for _, op := range args[i:] {
		varName, value, assigned := strings.Cut(op, "=")
		varName, appended := strings.CutSuffix(varName, "+")
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
			o.noteAppend(varName, value, appended)
			if !appended {
				o.noteValue(varName, value)
			}
		}
		if exports && optionEnvNames[varName] {
			o.exported[varName] = true
		}
	}
}

// setsAllexport reports whether set's arguments turn on allexport, or may:
// every option word before the positional parameters is read, both the `-`
// and the `+` ones; a in a `-` cluster turns it on, each o in a cluster takes
// its option name from the next word (`-eo allexport`), and an option word or
// name only known at run time (`set $x`, `set -o $name`) may be the one that
// does. Nothing turns it off again: `set +a` is not followed.
func setsAllexport(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if computedWord(a) {
			return true
		}
		if a == "--" || len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			return false // the positional parameters start
		}
		on := a[0] == '-'
		for _, r := range a[1:] {
			switch r {
			case 'a':
				if on {
					return true
				}
			case 'o':
				name := ""
				if i++; i < len(args) {
					// zsh also spells the option ALL_EXPORT.
					name = strings.ToLower(strings.ReplaceAll(args[i], "_", ""))
				}
				if computedWord(name) || (on && name == "allexport") {
					return true
				}
			}
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
	inner = optionReaderArgv(inner)
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
	// exportedWords counts the spliced words that come from the environment;
	// the ones written on the command were counted when its stage started.
	exportedWords := 0
	// After a binding the analysis cannot follow, HOME may be rebound too.
	javaCtx := javaPathCtx{cwd: payload.cwd, known: !payload.uncertain, written: payload.written,
		homeRebound: s.options.homeRebound || s.options.dynamic}
	judge := func(v optionEnvVar, value string) {
		if v.kind != optionArgv && v.kind != optionToolArgs {
			if _, isInline := inlineValues[v.name]; isInline {
				javaCtx.inline = inline
			} else {
				javaCtx.inline = nil
			}
			result.add(v.valueRisk(value, javaCtx))
			return
		}
		w, risk := optionValueWords(value)
		if risk != Safe {
			result.add(risk)
			return
		}
		if v.kind == optionToolArgs && toolArgsBenign(w) {
			return
		}
		words = append(words, w...)
		if _, isInline := inlineValues[v.name]; !isInline {
			exportedWords += len(w)
		}
	}
	for _, v := range readers {
		value, state := s.optionEnvValue(v.name, inlineValues)
		switch state {
		case optionAbsent:
			continue
		case optionUnresolved:
			result.add(v.unresolvedRisk())
			// Every value the command line gave the variable is one it may
			// still hold.
			for _, earlier := range s.options.values[v.name] {
				judge(v, earlier)
			}
			if s.options.dropped[v.name] {
				result.add(Unknown)
			}
		default:
			judge(v, value)
		}
		// An append to a value the analysis does not know: the appended
		// text is what the variable holds when it was unset before.
		if _, isInline := inlineValues[v.name]; !isInline && (state == optionUnresolved || computedWord(value)) {
			if tail, ok := s.options.tails[v.name]; ok {
				judge(v, tail)
			}
		}
	}
	if len(words) == 0 {
		return result
	}
	if len(words) > maxOptionEnvWords {
		result.add(Unknown)
		return result
	}
	// The splice budget of the whole analysis: past it the reader is not
	// analysed again.
	if s.work != nil {
		if s.work.splice += exportedWords; s.work.splice > maxOptionSpliceWords {
			result.add(Unknown)
			return result
		}
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
