package danger

import (
	"os"
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
