package danger

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// unsetExecEnvR4 removes the option-carrying and exec-controlling variables
// from the test process environment for the duration of t, so a name the
// runner happens to export cannot reach a reader.
func unsetExecEnvR4(t *testing.T) {
	t.Helper()
	for _, name := range []string{"TAR_OPTIONS", "ZIPOPT", "ZIP", "UNZIPOPT", "UNZIP", "MAVEN_ARGS", "MAVEN_CONFIG",
		"MAVEN_OPTS", "MAVEN_DEBUG_OPTS", "GRADLE_OPTS", "JAVA_OPTS", "SBT_OPTS", "ANT_OPTS", "CATALINA_OPTS",
		"JAVA_HOME", "JAVACMD", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "JAVA_TOOL_OPTIONS",
		"NODE_OPTIONS", "GIT_PAGER", "LESSOPEN", "GNUPGHOME"} {
		if old, ok := os.LookupEnv(name); ok {
			t.Setenv(name, old) // registers the restore
			os.Unsetenv(name)
		}
	}
}

// kotlinc is in the same toolchain table as mvn and gradle (`kotlinc
// -version` is safe), and its launcher script runs "${JAVACMD:=java}"
// $JAVA_OPTS, taking JAVACMD from $JAVA_HOME/bin/java when unset; the JDK's
// own tools on macOS are /usr/bin stubs that pick the JDK from $JAVA_HOME. The
// reader table listed neither, so these ran the named binary or agent while
// classifying safe. A shell running the Maven or Gradle wrapper script (`sh
// mvnw`) is the same launcher as `./mvnw`, yet only the command word was
// looked up, so the exported agent dropped from system_write to the
// code_execution of the script.
//
// Correct behaviour: every launcher that starts a JVM is a reader of the java
// binary variables (and, for a launcher script whose variables are not
// verified, of JAVA_OPTS too), and a shell running a launcher script reads
// what the script reads.
func TestRED_JVMLaunchersReadJavaEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	for cmd, ref := range map[string]string{
		`JAVA_OPTS=-javaagent:/tmp/x.jar kotlinc -version`:                                    `JAVA_OPTS=-javaagent:/tmp/x.jar gradle --version`,
		`export JAVA_OPTS=-javaagent:/tmp/x.jar; kotlinc -version`:                            `export JAVA_OPTS=-javaagent:/tmp/x.jar; gradle --version`,
		`JAVACMD=/tmp/j kotlinc -version`:                                                     `JAVACMD=/tmp/j mvnw -v`,
		`JAVA_HOME=/tmp/jdk kotlinc -version`:                                                 `JAVA_HOME=/tmp/jdk mvn -v`,
		`printf '#!/bin/sh\nid\n' > /tmp/j; chmod +x /tmp/j; JAVACMD=/tmp/j kotlinc -version`: `printf '#!/bin/sh\nid\n' > /tmp/j; chmod +x /tmp/j; JAVACMD=/tmp/j mvnw -v`,
		`JAVA_HOME=/tmp/jdk java -version`:                                                    `JAVA_HOME=/tmp/jdk mvn -v`,
		`JAVA_HOME=/tmp/jdk javac -version`:                                                   `JAVA_HOME=/tmp/jdk mvn -v`,
		`export JAVA_HOME=./jdk; jar tf x.jar`:                                                `export JAVA_HOME=./jdk; mvn -v`,
		`export MAVEN_OPTS=-javaagent:/tmp/x.jar; sh mvnw -v`:                                 `export MAVEN_OPTS=-javaagent:/tmp/x.jar; mvnw -v`,
		`export MAVEN_OPTS=-javaagent:/tmp/x.jar; bash ./mvnw -v`:                             `export MAVEN_OPTS=-javaagent:/tmp/x.jar; mvnw -v`,
		`GRADLE_OPTS=-javaagent:/tmp/x.jar sh -e gradlew --version`:                           `GRADLE_OPTS=-javaagent:/tmp/x.jar gradlew --version`,
		`MAVEN_ARGS='-s evil.xml' sh mvnw -v`:                                                 `MAVEN_ARGS='-s evil.xml' mvnw -v`,
		`MAVEN_CONFIG='-s evil.xml' bash -- mvnw -v`:                                          `MAVEN_CONFIG='-s evil.xml' mvnw -v`,
	} {
		want := Classify(ref)
		if Rank(want) < Rank(CodeExecution) {
			t.Fatalf("reference %q classified %s; premise broken", ref, want)
		}
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s (the verdict of %q)", cmd, got, want, ref)
		}
	}
	// Plain values keep the verdict of the script, and an inline script that
	// only names the launcher runs nothing that reads them.
	for cmd, plain := range map[string]string{
		`export MAVEN_OPTS=-Xmx1g; sh mvnw -v`:                      `sh mvnw -v`,
		`export MAVEN_ARGS='-s evil.xml'; sh -c 'echo mvnw'`:        `sh -c 'echo mvnw'`,
		`export JAVA_HOME=/usr/lib/jvm/java-17; bash gradlew build`: `bash gradlew build`,
	} {
		if got, want := Classify(cmd), Classify(plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, plain)
		}
	}
}

// jvmToolNamesR4 lists programs that start a JVM: the JDK's own tools, the
// build launchers and the JVM language launchers.
var jvmToolNamesR4 = []string{
	"java", "javac", "jar", "jarsigner", "javadoc", "javap", "jdeps", "jlink", "jmod", "jpackage", "jshell",
	"jdb", "jcmd", "jconsole", "jinfo", "jmap", "jps", "jstack", "jstat", "jstatd", "jhsdb", "jfr", "keytool",
	"serialver", "rmiregistry", "jwebserver", "jrunscript", "jdeprscan", "jimage", "native2ascii",
	"mvn", "mvnw", "mvnDebug", "mvnd", "gradle", "gradlew", "sbt", "sbtn", "ant", "catalina.sh",
	"kotlinc", "kotlinc-jvm", "kotlin", "scala", "scalac", "scala-cli", "amm", "groovy", "groovyc", "groovysh",
	"lein", "clojure", "clj", "jruby", "jirb", "jython", "jbang", "grails", "spring", "cs", "coursier",
}

// jdkNativeToolsR4 are the JDK's native launchers: they read no launcher
// script variables, but the macOS /usr/bin stubs choose the JDK from
// $JAVA_HOME.
var jdkNativeToolsR4 = map[string]bool{
	"java": true, "javac": true, "jar": true, "jarsigner": true, "javadoc": true, "javap": true, "jdeps": true,
	"jlink": true, "jmod": true, "jpackage": true, "jshell": true, "jdb": true, "jcmd": true, "jconsole": true,
	"jinfo": true, "jmap": true, "jps": true, "jstack": true, "jstat": true, "jstatd": true, "jhsdb": true,
	"jfr": true, "keytool": true, "serialver": true, "rmiregistry": true, "jwebserver": true, "jrunscript": true,
	"jdeprscan": true, "jimage": true, "native2ascii": true,
}

// launcherDoesNotReadR4 records, per launcher script, the variables its
// upstream script is verified not to read: the Maven and Ant scripts take
// their own *_OPTS rather than JAVA_OPTS, and the Gradle start scripts and
// catalina.sh set the java command themselves.
var launcherDoesNotReadR4 = map[string][]string{
	"mvn": {"JAVA_OPTS"}, "mvnw": {"JAVA_OPTS"}, "mvnDebug": {"JAVA_OPTS"}, "ant": {"JAVA_OPTS"},
	"gradle": {"JAVACMD"}, "gradlew": {"JAVACMD"}, "catalina.sh": {"JAVACMD"},
}

// Every program the package knows that starts a JVM, and that does not
// already classify unknown, reads the java binary variables; a launcher
// script whose variables are not verified also reads JAVACMD and JAVA_OPTS.
func TestRED_EveryKnownJVMLauncherReadsJavaEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	known := 0
	for _, name := range jvmToolNamesR4 {
		listed := safeCommands[name] || projectCodeTools[name] || codeEvalPrefixes[name] || writePrefixes[name]
		if !listed && Classify(name+" -version") == Unknown && Classify(name+" --version") == Unknown {
			continue // denied whatever its environment holds
		}
		known++
		cases := map[string]RiskClass{"JAVA_HOME=/tmp/jdk": CodeExecution}
		if !jdkNativeToolsR4[name] {
			cases["JAVACMD=/tmp/j"] = CodeExecution
			cases["JAVA_OPTS=-javaagent:/tmp/x.jar"] = SystemWrite
			for _, v := range launcherDoesNotReadR4[name] {
				for k := range cases {
					if len(k) > len(v) && k[:len(v)+1] == v+"=" {
						delete(cases, k)
					}
				}
			}
		}
		for assign, want := range cases {
			for _, form := range []string{assign + " " + name + " -version", "export " + assign + "; " + name + " -version"} {
				if got := Classify(form); Rank(got) < Rank(want) {
					t.Errorf("Classify(%q) = %s, want at least %s: %s starts a JVM", form, got, want, name)
				}
			}
		}
	}
	if known < 8 {
		t.Fatalf("only %d JVM launchers are known to the classifier; the table walk is not exercising it", known)
	}
}

// execVarCaseR4 is an exec-controlling variable, a value and a command that
// reads it.
type execVarCaseR4 struct{ name, value, reader string }

// execVarCasesR4 lists every variable the package treats as controlling what
// a program runs: the loader, hook and config names (envExecNames), pagers,
// git config injection, the shell startup variables, the code variables and
// the option-carrying variables, each with a reader.
func execVarCasesR4() []execVarCaseR4 {
	var out []execVarCaseR4
	names := make([]string, 0, len(envExecNames))
	for name := range envExecNames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		reader := "ls"
		switch {
		case strings.HasPrefix(name, "GIT_"):
			reader = "git log"
		case name == "NODE_OPTIONS":
			reader = "node -v"
		case strings.Contains(name, "JAVA"):
			reader = "java -version"
		case strings.HasPrefix(name, "LESS"):
			reader = "less x"
		}
		out = append(out, execVarCaseR4{name, "/tmp/x", reader})
	}
	return append(out,
		execVarCaseR4{"GIT_PAGER", "'sh -c id'", "git log"},
		execVarCaseR4{"MANPAGER", "/tmp/x", "man ls"},
		execVarCaseR4{"GIT_CONFIG_KEY_0", "core.pager", "git log"},
		execVarCaseR4{"GIT_CONFIG_VALUE_0", "'sh -c id'", "git log"},
		execVarCaseR4{"ENV", "/tmp/x", "sh -c id"},
		execVarCaseR4{"SHELL", "/tmp/x", "man ls"},
		execVarCaseR4{"GIT_TRACE2", "/tmp/x", "git status"},
		execVarCaseR4{"HOME", "/tmp", "bash -l"},
		execVarCaseR4{"ZDOTDIR", "/tmp", "zsh -c id"},
		execVarCaseR4{"XDG_CONFIG_HOME", "/tmp", "git log"},
		execVarCaseR4{"PROMPT_COMMAND", "'rm -rf /home/user'", "bash -i"},
		execVarCaseR4{"PS1", "'$(id)'", "bash -i"},
		execVarCaseR4{"GNUPGHOME", "/tmp/g", "gpg --list-keys"},
		execVarCaseR4{"TAR_OPTIONS", "--to-command=sh", "tar xf x.tar"},
		execVarCaseR4{"ZIPOPT", "-m", "zip a.zip f"},
		execVarCaseR4{"UNZIP", "'-o -d /etc'", "unzip a.zip"},
		execVarCaseR4{"MAVEN_ARGS", "'-s evil.xml'", "mvn -v"},
		execVarCaseR4{"MAVEN_CONFIG", "'-s evil.xml'", "mvnw -v"},
		execVarCaseR4{"MAVEN_OPTS", "-javaagent:/tmp/x.jar", "mvn -v"},
		execVarCaseR4{"GRADLE_OPTS", "-javaagent:/tmp/x.jar", "gradle --version"},
		execVarCaseR4{"JAVA_OPTS", "-javaagent:/tmp/x.jar", "kotlinc -version"},
		execVarCaseR4{"SBT_OPTS", "-javaagent:/tmp/x.jar", "sbt -version"},
		execVarCaseR4{"JAVA_HOME", "/tmp/jdk", "mvn -v"},
		execVarCaseR4{"JAVACMD", "/tmp/j", "mvnw -v"},
	)
}

// bindFormsR4 are the ways a statement hands a variable to the commands after
// it; op is "=" or "+=".
var bindFormsR4 = map[string]func(name, op, value, reader string) string{
	"export":          func(n, op, v, r string) string { return "export " + n + op + v + "; " + r },
	"declare -x":      func(n, op, v, r string) string { return "declare -x " + n + op + v + "; " + r },
	"typeset -x":      func(n, op, v, r string) string { return "typeset -x " + n + op + v + "; " + r },
	"local -x":        func(n, op, v, r string) string { return "f() { local -x " + n + op + v + "; " + r + "; }; f" },
	"readonly+export": func(n, op, v, r string) string { return "readonly " + n + op + v + "; export " + n + "; " + r },
	"declare+export":  func(n, op, v, r string) string { return "declare " + n + op + v + "; export " + n + "; " + r },
	"export --":       func(n, op, v, r string) string { return "export -- " + n + op + v + "; " + r },
	"inline":          func(n, op, v, r string) string { return n + op + v + " " + r },
}

// `export NAME+=value` names NAME exactly like `export NAME=value` (with NAME
// unset, bash and sh assign), but the exec-controlling check on the operands
// of export, declare/typeset/local -x and readonly took only NAME=value words,
// so `export LD_PRELOAD+=/tmp/x.so; ls` was safe; an append to an option
// variable whose earlier value was unknown held only "a run-time value",
// which dropped the --to-command the same words carry after `=`; and a value
// set by readonly or a declare without -x and exported later by name was never
// judged. The denylist's own variable tracking kept the value from before an
// append, so `cmd=git; export cmd+=' push'; $cmd` missed a `git push` entry.
//
// Correct behaviour: every += form classifies at least as high as its = form,
// a declaration that does not export judges the exec-controlling names it
// sets as a plain assignment of them is judged, and the denylist follows the
// append.
func TestRED_AppendFormsJudgedLikeAssignments(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	for _, tc := range []struct{ cmd, ref string }{
		{`export LD_PRELOAD+=/tmp/x.so; ls`, `export LD_PRELOAD=/tmp/x.so; ls`},
		{`declare -x LD_PRELOAD+=/tmp/x.so; ls`, `export LD_PRELOAD=/tmp/x.so; ls`},
		{`typeset -x LD_PRELOAD+=/tmp/x.so; ls`, `export LD_PRELOAD=/tmp/x.so; ls`},
		{`export -- LD_PRELOAD+=/tmp/x.so; ls`, `export LD_PRELOAD=/tmp/x.so; ls`},
		{`f() { local -x LD_PRELOAD+=/tmp/x.so; ls; }; f`, `export LD_PRELOAD=/tmp/x.so; ls`},
		{`export LD_LIBRARY_PATH+=/tmp; ls`, `export LD_LIBRARY_PATH=/tmp; ls`},
		{`export JAVA_TOOL_OPTIONS+=-javaagent:/tmp/x.jar; java -version`, `export JAVA_TOOL_OPTIONS=-javaagent:/tmp/x.jar; java -version`},
		{`export NODE_OPTIONS+='--require /tmp/x.js'; node -v`, `export NODE_OPTIONS='--require /tmp/x.js'; node -v`},
		{`export GIT_PAGER+='sh -c id'; git log`, `export GIT_PAGER='sh -c id'; git log`},
		{`export LESSOPEN+='|sh -c id %s'; less x`, `export LESSOPEN='|sh -c id %s'; less x`},
		{`base64 -d <<< UEsDBA== > /tmp/a.so; export LD_PRELOAD+=/tmp/a.so; ls`, `base64 -d <<< UEsDBA== > /tmp/a.so; export LD_PRELOAD=/tmp/a.so; ls`},
		{`readonly LD_PRELOAD=/tmp/x.so; export LD_PRELOAD; ls`, `LD_PRELOAD=/tmp/x.so; export LD_PRELOAD; ls`},
		{`declare LD_PRELOAD+=/tmp/x.so; export LD_PRELOAD; ls`, `LD_PRELOAD=/tmp/x.so; export LD_PRELOAD; ls`},
		{`export TAR_OPTIONS+=--to-command=sh; tar xf x.tar`, `export TAR_OPTIONS=--to-command=sh; tar xf x.tar`},
	} {
		want := Classify(tc.ref)
		if Rank(want) < Rank(SystemWrite) && !strings.Contains(tc.ref, "TAR_OPTIONS") {
			t.Fatalf("reference %q classified %s; premise broken", tc.ref, want)
		}
		if got := Classify(tc.cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s (the verdict of %q)", tc.cmd, got, want, tc.ref)
		}
	}
	forms := make([]string, 0, len(bindFormsR4))
	for form := range bindFormsR4 {
		forms = append(forms, form)
	}
	sort.Strings(forms)
	for _, c := range execVarCasesR4() {
		for _, form := range forms {
			build := bindFormsR4[form]
			assign, appendCmd := build(c.name, "=", c.value, c.reader), build(c.name, "+=", c.value, c.reader)
			want := Classify(assign)
			if got := Classify(appendCmd); Rank(got) < Rank(want) {
				t.Errorf("Classify(%q) = %s, want at least %s (the verdict of %q)", appendCmd, got, want, assign)
			}
			// A name judged by itself is judged however the value is bound.
			if envExecNames[c.name] && Rank(want) < Rank(SystemWrite) {
				t.Errorf("Classify(%q) = %s, want at least system_write", assign, want)
			}
		}
	}
	if !denylistMatch(`cmd=git; export cmd+=' push'; $cmd`, []string{"git push"}) {
		t.Error("denylist `git push` must match a value completed by an append")
	}
	if denylistMatch(`cmd=git; export cmd+=' status'; $cmd`, []string{"git push"}) {
		t.Error("denylist `git push` must not match `git status`")
	}
}
