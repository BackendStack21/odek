package danger

import (
	"os"
	"strings"
	"testing"
	"time"
)

// unsetOptionEnvForTest removes the option-carrying variables from the test
// process environment for the duration of t: a name the process exports
// reaches a reader after a plain assignment, so the expectations below assume
// none is set.
func unsetOptionEnvForTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{"TAR_OPTIONS", "ZIPOPT", "ZIP", "UNZIPOPT", "UNZIP", "MAVEN_ARGS", "MAVEN_CONFIG",
		"MAVEN_OPTS", "MAVEN_DEBUG_OPTS", "GRADLE_OPTS", "JAVA_OPTS", "SBT_OPTS", "ANT_OPTS", "CATALINA_OPTS",
		"JAVA_HOME", "JAVACMD"} {
		if old, ok := os.LookupEnv(name); ok {
			t.Setenv(name, old) // registers the restore
			os.Unsetenv(name)
		}
	}
}

// Option-carrying variables are read by name exactly as the tools spell
// them. The values were keyed by strings.ToUpper(name), so a lower-case
// shell variable no tool reads (maven_args, tar_options) overwrote the real
// variable's value, deterministically for an inline assignment and by map
// iteration order for an earlier statement, and ordinary lower-case script
// variables (zip, java_args) were judged as tool options.
//
// Correct behaviour: only the exact names count, and the verdict does not
// depend on map order.
func TestRED_OptionEnvNamesAreCaseSensitive(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	for cmd, want := range map[string]RiskClass{
		`export MAVEN_ARGS='-s /tmp/evil.xml'; maven_args=-v mvn -v`:             CodeExecution,
		`export MAVEN_ARGS='-s /tmp/evil.xml'; maven_args=-v; mvn -v`:            CodeExecution,
		`read TAR_OPTIONS < f; export TAR_OPTIONS; tar_options=-v tar xf x.tar`:  SystemWrite,
		`read TAR_OPTIONS < f; export TAR_OPTIONS; tar_options=-v; tar xf x.tar`: SystemWrite,
	} {
		seen := map[RiskClass]int{}
		for i := 0; i < 200; i++ {
			seen[Classify(cmd)]++
		}
		if len(seen) != 1 {
			t.Errorf("Classify(%q) is not deterministic: %v", cmd, seen)
		}
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
	for _, cmd := range []string{
		`zip=$(ls dist/*.zip | head -1); echo "$zip"`,
		`java_args="-jar app.jar"; echo hi`,
		`tar_options=--to-command=sh; export tar_options; echo hi`,
	} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe (no tool reads a lower-case name)", cmd, got)
		}
	}
}

// A variable matters only to the programs that read it, and only when such a
// program runs later with the variable in its environment. Every export of an
// option-carrying variable was judged on its own (as if the tool ran), a bare
// operand word (`ls ZIP`) counted as binding the variable, and an unexported
// shell variable used as a command path (`UNZIP=/usr/bin/unzip; $UNZIP ...`)
// was spliced in as options, so unrelated commands escalated.
//
// Correct behaviour: those commands keep the verdict of the command without
// the variable; the reader still escalates.
func TestRED_OptionEnvReachesOnlyItsReaders(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	for _, tc := range []struct{ cmd, plain string }{
		{`export JAVA_OPTS="$JAVA_OPTS -Xmx1g"; echo hi`, `echo hi`},
		{`export MAVEN_OPTS="-Xmx1g -XX:+UseG1GC --add-opens java.base/java.lang=ALL-UNNAMED"; echo hi`, `echo hi`},
		{`export MAVEN_OPTS=-javaagent:/tmp/x.jar; echo hi`, `echo hi`},
		{`export TAR_OPTIONS=--to-command=sh; echo hi`, `echo hi`},
		{`JAVA_OPTS="-Dnext.token=1" true`, `true`},
		{`ls ZIP; zip -r out.zip src`, `ls x; zip -r out.zip src`},
		{`UNZIP=/usr/bin/unzip; $UNZIP -o a.zip`, `/usr/bin/unzip -o a.zip`},
		{`TAR_OPTIONS=--to-command=sh; tar xf x.tar`, `tar xf x.tar`},
		{`JAVA_OPTS=-Djava.ext.dirs=/tmp java -jar app.jar`, `java -jar app.jar`},
		{`JAVACMD=./evil mvn -v`, `mvn -v`},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", tc.cmd, got, want, tc.plain)
		}
	}
	// A program the analysis cannot see into keeps its own verdict, which
	// already says it runs code of its own choosing.
	for _, tc := range []struct{ cmd, plain string }{
		{`export MAVEN_OPTS=-javaagent:/tmp/x.jar; ./build.sh`, `./build.sh`},
		{`export TAR_OPTIONS=--to-command=sh; make`, `make`},
		{`export MAVEN_OPTS=-javaagent:/tmp/x.jar; bash build.sh`, `bash build.sh`},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); Rank(got) < Rank(want) || Rank(want) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least %s (the verdict of %q, at least code_execution)", tc.cmd, got, want, tc.plain)
		}
	}
	// The readers, reached directly or through a launcher, still escalate.
	for cmd, want := range map[string]RiskClass{
		`export TAR_OPTIONS=--to-command=sh; tar xf x.tar`:                        CodeExecution,
		`TAR_OPTIONS=--to-command=sh; export TAR_OPTIONS; tar xf x.tar`:           CodeExecution,
		`export TAR_OPTIONS=--to-command=sh; echo x.tar | xargs tar xf`:           CodeExecution,
		`export TAR_OPTIONS=--to-command=sh; timeout 10 tar xf x.tar`:             CodeExecution,
		`export TAR_OPTIONS=--to-command=sh; command tar xf x.tar`:                CodeExecution,
		`export TAR_OPTIONS=--to-command=sh; bash -c 'tar xf x.tar'`:              CodeExecution,
		`TAR_OPTIONS=--to-command=sh bash -c 'tar xf x.tar'`:                      CodeExecution,
		`env TAR_OPTIONS=--to-command=sh tar xf x.tar`:                            CodeExecution,
		`export MAVEN_OPTS=-javaagent:/tmp/x.jar; mvnw -v`:                        SystemWrite,
		`GRADLE_OPTS=-javaagent:/tmp/x.jar gradle --version`:                      SystemWrite,
		`export JAVA_OPTS="$JAVA_OPTS -Xmx1g"; gradle --version`:                  SystemWrite,
		`f() { tar xf x.tar; }; TAR_OPTIONS=--to-command=sh f`:                    SystemWrite,
		`export TAR_OPTIONS=-v; eval 'TAR_OPTIONS=--to-command=sh'; tar xf x.tar`: SystemWrite,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
}

// A variable the process environment already exports stays exported in the
// shell, so a plain assignment reaches the reader.
func TestRED_OptionEnvInheritedExportReachesReader(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	cmd := `TAR_OPTIONS=--to-command=sh; tar xf x.tar`
	if got := Classify(cmd); got != Classify(`tar xf x.tar`) {
		t.Fatalf("Classify(%q) = %s without TAR_OPTIONS in the environment", cmd, got)
	}
	t.Setenv("TAR_OPTIONS", "")
	if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
		t.Errorf("Classify(%q) = %s with TAR_OPTIONS exported by the environment, want at least code_execution", cmd, got)
	}
	// A value the command line never assigns is the inherited one.
	if got, want := Classify(`tar xf x.tar`), Classify(`echo x; tar xf x.tar`); got != want {
		t.Errorf("tar with an inherited TAR_OPTIONS classified %s, want %s", got, want)
	}
}

// Benign values of a variable in front of its own reader keep the verdict of
// the reader alone. MAVEN_ARGS words were spliced into the argv, which makes
// `mvn -q -v` (code_execution: no longer a plain version query), and the JVM
// property check matched code-ish substrings anywhere in the name ('ext' in
// next/context/text, 'agent', 'home', 'lib', 'path').
//
// Correct behaviour: listed Maven output and mode flags change nothing, and
// JVM system properties are matched by exact name or documented prefix
// against the properties that load code or point at configuration naming
// code. Both sides of the table are pinned.
func TestRED_OptionEnvBenignValuesKeepReaderVerdict(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	for _, tc := range []struct{ cmd, plain string }{
		{`MAVEN_OPTS=-Xmx2g mvn -v`, `mvn -v`},
		{`MAVEN_ARGS="-q" mvn -v`, `mvn -v`},
		{`MAVEN_ARGS=-q mvn -v`, `mvn -v`},
		{`MAVEN_ARGS='-B -ntp -T4 --color=always' mvn -v`, `mvn -v`},
		{`export MAVEN_ARGS=-e; mvn --version`, `mvn --version`},
		{`MAVEN_OPTS=-Dhttp.agent=x mvn -v`, `mvn -v`},
		{`MAVEN_OPTS='-Dcontext=dev -Dnext.token=1 -Dtext.mode=a' mvn -v`, `mvn -v`},
		{`MAVEN_OPTS='-Dapp.home=/opt/app -Dlib.version=2 -Dpath.style=x' mvn -v`, `mvn -v`},
		{`MAVEN_OPTS='-XX:MaxRAMPercentage=75.0 -ea --enable-preview' mvn -v`, `mvn -v`},
		{`MAVEN_OPTS='--add-opens java.base/java.lang=ALL-UNNAMED' mvn -v`, `mvn -v`},
		{`GRADLE_OPTS='-Dorg.gradle.jvmargs=-Xmx2g' gradle --version`, `gradle --version`},
		{`JAVA_OPTS=-Dcontext=dev java -jar app.jar`, `java -jar app.jar`},
		{`JAVA_HOME=/usr/lib/jvm/java-17 mvn -v`, `mvn -v`},
		{`JAVACMD=java mvnw -v`, `mvnw -v`},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", tc.cmd, got, want, tc.plain)
		}
	}
	for _, value := range []string{
		"-javaagent:./x.jar", "-agentpath:/tmp/x.so", "-agentlib:jdwp=transport=dt_socket,server=y",
		"-Xrunjdwp:transport=dt_socket", "-Xbootclasspath/a:./x.jar", "-XX:OnError=./x",
		"-XX:OnOutOfMemoryError=./x", "-XX:Flags=./flags", "-XX:VMOptionsFile=./opts",
		"-XX:SharedArchiveFile=./x.jsa", "@./args", "-cp ./x", "-classpath ./x", "--class-path ./x",
		"-p ./mods", "--module-path ./mods", "-jar ./x.jar", "-m x/Main", "EvilMain",
		"-Djava.ext.dirs=./x", "-Djava.library.path=./x", "-Djava.system.class.loader=Evil",
		"-Djava.security.manager=Evil", "-Djava.security.policy=./p", "-Duser.home=./h",
		"-Dmaven.ext.class.path=./x.jar", "-Dorg.gradle.jvmargs=-javaagent:./x.jar",
		"-Dgradle.user.home=./g", "-Dsbt.global.base=./s", "-Djdk.module.patch.0=x=./p",
	} {
		cmd := "MAVEN_OPTS='" + value + "' mvn -v"
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for cmd, want := range map[string]RiskClass{
		`MAVEN_ARGS='-s /tmp/evil.xml' mvn -v`:       CodeExecution,
		`MAVEN_ARGS=--settings=/tmp/evil.xml mvn -v`: CodeExecution,
		`MAVEN_ARGS='-gs ./g.xml' mvn -v`:            CodeExecution,
		`MAVEN_ARGS='-f /tmp/pom.xml' mvn -v`:        CodeExecution,
		`MAVEN_ARGS='-q deploy' mvn -V`:              CodeExecution,
		`MAVEN_CONFIG='-s ./x.xml' mvnw -v`:          CodeExecution,
		`JAVA_HOME=/tmp/evil mvn -v`:                 CodeExecution,
		`JAVA_HOME=./jdk gradle --version`:           CodeExecution,
		`export JAVA_HOME=$(cat f); mvn -v`:          CodeExecution,
		`JAVACMD=./evil mvnw -v`:                     CodeExecution,
		`JAVACMD=/tmp/java ant -version`:             CodeExecution,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
}

// `export NAME+=word` and `declare -x NAME+=word` append to NAME (as plain
// `NAME+=word` does), but the operand reader kept the `+` and looked up
// `NAME+`, so the variable kept its earlier value: every tracked variable
// (HOME for a login shell's startup files, a path used as a write target, the
// option-carrying variables) was judged with a stale value.
//
// Correct behaviour: an append to a known value yields the joined value; an
// append to an unknown one leaves it unknown.
func TestRED_AppendDeclarationUpdatesTrackedValue(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	for cmd, ref := range map[string]string{
		`export TAR_OPTIONS=-v; export TAR_OPTIONS+=' --to-command=sh'; tar xf x.tar`:     `tar --to-command=sh xf x.tar`,
		`export TAR_OPTIONS=-v; declare -x TAR_OPTIONS+=' --to-command=sh'; tar xf x.tar`: `tar --to-command=sh xf x.tar`,
		`export TAR_OPTIONS=-v TAR_OPTIONS+=' --to-command=sh'; tar xf x.tar`:             `tar --to-command=sh xf x.tar`,
		`export UNZIP=-q; export UNZIP+=' -d /etc'; unzip a.zip`:                          `unzip -d /etc a.zip`,
		`export MAVEN_OPTS=-Xmx1g; export MAVEN_OPTS+=' -javaagent:/tmp/x.jar'; mvn -v`:   `MAVEN_OPTS=-javaagent:/tmp/x.jar mvn -v`,
		`export HOME=/home/user; export HOME+=/x; bash -l`:                                `HOME=/home/user/x bash -l`,
		`declare -x HOME=/home/user; declare -x HOME+=/x; bash -l`:                        `HOME=/home/user/x bash -l`,
		`export D=/home/user/w; export D+=/../../../etc; touch "$D/f"`:                    `touch /etc/f`,
	} {
		want := Classify(ref)
		if Rank(want) <= Rank(LocalWrite) {
			t.Fatalf("reference %q classified %s; test premise broken", ref, want)
		}
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s (the verdict of %q)", cmd, got, want, ref)
		}
	}
	// An append that keeps the value harmless keeps the verdict.
	if got, want := Classify(`export HOME=/home/user; export HOME+=''; bash -l`), Classify(`bash -l`); got != want {
		t.Errorf("an empty append to HOME classified %s, want %s", got, want)
	}
}

// ${NAME:=word} (and ${NAME=word} on an unset NAME) assigns NAME, but the
// analysis kept the value NAME had before, so a later use was judged with it:
// `export TAR_OPTIONS=; : ${TAR_OPTIONS:=--to-command=sh}; tar xf x.tar` ran
// sh for each member yet classified local_write.
//
// Correct behaviour: the assignment drops the known value, so later uses are
// unresolved (and a reader of an option-carrying variable fails closed).
func TestRED_DefaultAssignmentDropsStaleValue(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	for cmd, want := range map[string]RiskClass{
		`export TAR_OPTIONS=; : ${TAR_OPTIONS:=--to-command=sh}; tar xf x.tar`: Classify(`tar --to-command=sh xf x.tar`),
		`D=; : ${D:=/etc}; touch "$D/f"`:                                       Classify(`touch /etc/f`),
		`D=; echo "${D:=/etc}"; touch "$D/f"`:                                  Classify(`touch /etc/f`),
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
	// The default does not apply to a variable that is set (and, without the
	// colon, to one that is set but empty): the value stands.
	for cmd, plain := range map[string]string{
		`D=/home/user/w; : ${D:=/etc}; touch "$D/f"`:                          `touch /home/user/w/f`,
		`export TAR_OPTIONS=; : ${TAR_OPTIONS=--to-command=sh}; tar xf x.tar`: `tar xf x.tar`,
	} {
		if got, want := Classify(cmd), Classify(plain); got != want {
			t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, plain)
		}
	}
}

// Each run of a reader with an option-carrying variable was analysed again
// with the value spliced in, and the per-stage check classified the spliced
// argv, both without bounding the value: a 60 KB command of ten
// `TAR_OPTIONS='-v ×2000' tar` stages took tens of seconds (every spliced word
// is resolved as a path, several times per stage), and a 600-byte value of
// 200 words cost over ten times a plain run.
//
// Correct behaviour: a value over maxOptionEnvWords words or
// maxOptionEnvBytes bytes is not spliced and classifies unknown, so the cost
// stays bounded and the verdict is never lower.
func TestRED_OptionEnvSpliceBounded(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetOptionEnvForTest(t)
	value := func(n int) string { return strings.TrimSpace(strings.Repeat("-v ", n)) }
	for _, cmd := range []string{
		"TAR_OPTIONS='" + value(200) + "' tar xf x.tar",
		"export TAR_OPTIONS='" + value(200) + "'; tar xf x.tar",
		"MAVEN_OPTS='" + strings.TrimSpace(strings.Repeat("-Xmx1g ", 200)) + "' mvn -v",
		"TAR_OPTIONS='--warning=" + strings.Repeat("x", 5000) + "' tar xf x.tar",
	} {
		if got := Classify(cmd); got != Unknown {
			t.Errorf("Classify(%d-byte value over the bound) = %s, want unknown", len(cmd), got)
		}
	}
	// Values within the bound are still judged.
	for cmd, want := range map[string]RiskClass{
		"TAR_OPTIONS='" + value(60) + "' tar xf x.tar":                 Classify("tar xf x.tar"),
		"TAR_OPTIONS='" + value(60) + " --to-command=sh' tar xf x.tar": CodeExecution,
	} {
		if got := Classify(cmd); Rank(got) < Rank(want) || (want != CodeExecution && got != want) {
			t.Errorf("Classify(%.60q…) = %s, want %s", cmd, got, want)
		}
	}
	big := strings.Repeat("TAR_OPTIONS='"+value(2000)+"' tar; ", 10)
	if len(big) > MaxCommandBytes {
		t.Fatalf("input %d bytes over MaxCommandBytes", len(big))
	}
	start := time.Now()
	got := Classify(big)
	elapsed := time.Since(start)
	if got != Unknown {
		t.Errorf("Classify of the %d-byte command = %s, want unknown", len(big), got)
	}
	if !raceEnabled && !testing.Short() && elapsed > boundedAnalyzeSlow {
		t.Errorf("Classify of a %d-byte TAR_OPTIONS command took %v (bound %v)", len(big), elapsed, boundedAnalyzeSlow)
	}
}
