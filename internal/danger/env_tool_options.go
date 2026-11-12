package danger

import (
	"regexp"
	"strings"
)

// Option-carrying environment variables.
//
// Some tools read extra command-line options from an environment variable:
// GNU tar prepends $TAR_OPTIONS, Info-ZIP zip and unzip read $ZIPOPT/$ZIP and
// $UNZIPOPT/$UNZIP, and the JVM launchers behind Maven, Gradle, sbt and the
// like hand $MAVEN_OPTS, $GRADLE_OPTS, $JAVA_OPTS, … to the JVM they start,
// even for `mvn -v`. The value is classified as if its words were written on
// the tool's command line, so an exec option (`--to-command`, `-TT`,
// `--checkpoint-action=exec=`) or a source-deleting one (`--remove-files`,
// `-m`) in the variable is judged exactly like the same option typed out. A
// value that cannot be resolved statically fails closed.

// optionEnvTools maps an option-carrying variable to the tool that reads it.
var optionEnvTools = map[string]string{
	"TAR_OPTIONS": "tar",
	"ZIPOPT":      "zip", "ZIP": "zip",
	"UNZIPOPT": "unzip", "UNZIP": "unzip",
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
	tool, ok := optionEnvTools[upper]
	if !ok || strings.TrimSpace(val) == "" {
		return Safe
	}
	if optionValueUnresolved(val) {
		return SystemWrite
	}
	words := tokenize(val)
	base := classifyStage([]string{tool}, false)
	risk := Safe
	consider := func(tokens []string) {
		if got := classifyStage(tokens, false); Rank(got) > Rank(base) {
			risk = worstOf(risk, got)
		}
	}
	consider(append([]string{tool}, words...))
	if len(inner) > 0 && commandName(inner[0]) == tool {
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
