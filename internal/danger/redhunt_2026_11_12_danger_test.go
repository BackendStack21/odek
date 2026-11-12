package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `hash -p FILE NAME` binds NAME to FILE in the shell's command table: every
// later bare NAME in the same shell runs FILE (sh -c 'hash -p /bin/echo ls;
// ls x' prints "x" with bash, macOS /bin/sh and zsh's `hash NAME=FILE`). The
// classifier rates `hash` as a safe builtin and the later `ls` as `ls`, so a
// repository-planted payload or `rm` runs under a safe name with auto-allow.
// The nested-script gate already treats `hash -p` as computed
// (readledger_nested.go isComputed), and alias.go tracks the same kind of
// rebinding for aliases; the top-level classifier does neither.
//
// Correct behaviour: a command that rebinds a name and then uses it is at
// least as risky as running the bound file directly (`./payload` alone is
// unknown, `rm -rf ~` destructive). At minimum it must not auto-allow.
func TestRED_HashPRebindsCommandNameToArbitraryProgram(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("\x7fELF\x00\x00planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Classify("./payload"); Rank(got) < Rank(CodeExecution) {
		t.Fatalf("reference ./payload classified %s; test premise broken", got)
	}
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		// Runs the planted ./payload when `ls` is invoked.
		"hash -p ./payload ls; ls",
		"hash -p ./payload -- ls; ls",
		// Runs rm -rf ~ under the name ls; no literal system path in sight.
		`hash -p "$(command -v rm)" ls; ls -rf ~`,
		"hash -p $(which rm) ls; ls -rf ~",
		"hash -p ../../../../../../bin/rm ls; ls -rf ~",
	} {
		cls := Classify(cmd)
		action := cfg.ActionForCommand(cmd)
		if Rank(cls) < Rank(CodeExecution) || action == Allow {
			t.Errorf("%q: class=%s action=%s; want >= code_execution and not auto-allowed (the later bare name runs the bound program)", cmd, cls, action)
		}
	}
}

// The nested-script gate (readledger_nested.go) inspects a licensed script for
// local programs it launches. program() skips listed wrappers by looking up
// the word as written (shellWrappers[bare]), not its command name, so a wrapper
// spelled by path or quoted (/usr/bin/env, "/usr/bin/env", /usr/bin/nohup) is
// taken as the program and the launched binary after it is never examined.
// `time` is a listed keyword but its -p option is then returned as the
// program, with the same effect. These are listed wrappers mis-parsed, not
// the documented "wrapper outside its lists" limit; the top-level classifier
// resolves wrappers by basename. The script-level wrapper forms (`env
// ./code_policies`, `nohup ./code_policies`) do gate.
//
// Correct behaviour: the read script stays licensed only for itself; the
// unread native binary it launches (or the run-time-chosen program) gates as
// unread_exec, exactly like the bare-wrapper spellings.
func TestRED_NestedGateMissesLaunchBehindPathSpelledWrapper(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	// Sanity: the same launch behind the bare wrapper name is caught.
	dir := nestedFixture(t, "#!/bin/sh\nenv ./code_policies\n")
	RecordRead(filepath.Join(dir, "security.sh"))
	if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
		t.Fatalf("premise: bare env form class=%s targets=%v", cls, targets)
	}
	for name, tc := range map[string]struct{ script, want string }{
		"absolute env":      {"#!/bin/sh\n/usr/bin/env ./code_policies\n", "code_policies"},
		"quoted env":        {"#!/bin/sh\n\"/usr/bin/env\" ./code_policies\n", "code_policies"},
		"absolute nohup":    {"#!/bin/sh\n/usr/bin/nohup ./code_policies\n", "code_policies"},
		"time -p":           {"#!/bin/bash\ntime -p ./code_policies\n", "code_policies"},
		"absolute env, var": {"#!/bin/sh\n/usr/bin/env \"$TOOL\" --scan\n", "security.sh"},
		"time -p, var":      {"#!/bin/bash\ntime -p \"$TOOL\" --scan\n", "security.sh"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, tc.script)
			RecordRead(filepath.Join(dir, "security.sh"))
			cls, targets := ClassifyScriptGate("bash security.sh")
			if cls != UnreadExec || !strings.Contains(targetNames(targets), tc.want) {
				t.Errorf("script %q: class=%s targets=%v; want unread_exec naming %s", tc.script, cls, targets, tc.want)
			}
		})
	}
}

// The nested gate's shell scanner (shellScan in readledger_nested.go) is not
// quote-aware in three places: skipBalanced('{','}') ends ${...} at a quoted
// '}', word() does not know ANSI-C $'...' quoting (so $'\” leaves a quote
// open), and the [[ ... ]] skip stops at the first "]]" even inside quotes.
// Each leaves an unmatched quote that swallows the rest of the script, and an
// unterminated quote at end of text does not fail the parse, so commands
// after it are never seen. bash runs them (verified: `echo ${x:-'}'} ; echo
// RAN`, `echo $'\” ; echo RAN` and `[[ $x == "]]" ]]; echo RAN` all print RAN).
// A hostile but readable script therefore launches an unread binary while the
// gate reports nothing.
//
// Correct behaviour: the launch after the construct is gated as unread_exec,
// or the parse fails closed (the script itself stays a target).
func TestRED_NestedGateScannerQuoteDesyncHidesLaunch(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for name, script := range map[string]string{
		"quoted brace in default":   "#!/bin/bash\necho ${x:-'}'} ; ./code_policies\n",
		"quoted brace in pattern":   "#!/bin/bash\necho ${x//'}'/a} ; ./code_policies\n",
		"ansi-c escaped quote":      "#!/bin/bash\necho $'\\'' ; ./code_policies\n",
		"quoted ]] inside [[ ]]":    "#!/bin/bash\n[[ $x == \"]]\" ]]; ./code_policies\n",
		"ansi-c then computed name": "#!/bin/bash\necho $'\\'' ; \"$TOOL\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			cls, targets := ClassifyScriptGate("bash security.sh")
			if cls != UnreadExec || len(targets) == 0 {
				t.Errorf("script %q: class=%s targets=%v; want unread_exec (the launch after the quoted construct runs in bash)", script, cls, targets)
			}
		})
	}
}

// Tools read extra command-line options from environment variables: GNU tar
// prepends $TAR_OPTIONS, Info-ZIP zip inserts $ZIPOPT (verified on macOS:
// ZIPOPT=-m deletes the source, ZIPOPT='-TT ./evil' runs ./evil), and the
// Maven/Gradle launchers pass $MAVEN_OPTS/$GRADLE_OPTS to the JVM they start
// even for -v. envAssignmentRisk only knows a fixed list (envExecNames: the
// JVM's JAVA_TOOL_OPTIONS, NODE_OPTIONS, PERL5OPT, …), so the option-carrying
// variables of these tools are ignored and the command keeps the verdict of
// its option-free form. The CLI spellings of the same options are already
// code_execution/destructive (earlier tar --to-command, zip -TT, zip -m,
// tar --remove-files and JAVA_TOOL_OPTIONS fixes); the env route is the
// sibling those fixes missed. Plain-value forms carry no |;& so the
// "armed value" heuristic does not fire either.
//
// Correct behaviour: the env spelling is at least as risky as the CLI one.
func TestRED_ToolOptionEnvVarsBypassOptionClassification(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ env, cli string }{
		{"TAR_OPTIONS=--to-command=./evil tar xf a.tar", "tar xf a.tar --to-command=./evil"},
		{"TAR_OPTIONS=--use-compress-program=./evil tar xf a.tar", "tar xf a.tar --use-compress-program=./evil"},
		{"TAR_OPTIONS='--checkpoint=1 --checkpoint-action=exec=./evil' tar cf a.tar x", "tar cf a.tar x --checkpoint=1 --checkpoint-action=exec=./evil"},
		{"TAR_OPTIONS=--remove-files tar cf a.tar /home/user/project", "tar cf a.tar --remove-files /home/user/project"},
		{"export TAR_OPTIONS=--to-command=./evil; tar xf a.tar", "tar xf a.tar --to-command=./evil"},
		{"ZIPOPT='-TT ./evil' zip -T a.zip x", "zip -T -TT ./evil a.zip x"},
		{"ZIPOPT=-m zip a.zip /home/user/project", "zip -m a.zip /home/user/project"},
		{"MAVEN_OPTS='-javaagent:./evil.jar' mvn -v", "JAVA_TOOL_OPTIONS='-javaagent:./evil.jar' mvn -v"},
		{"GRADLE_OPTS='-javaagent:./evil.jar' gradle -v", "JAVA_TOOL_OPTIONS='-javaagent:./evil.jar' gradle -v"},
	} {
		ref := Classify(tc.cli)
		if Rank(ref) < Rank(CodeExecution) {
			t.Fatalf("reference %q classified %s; test premise broken", tc.cli, ref)
		}
		if got := Classify(tc.env); Rank(got) < Rank(ref) {
			t.Errorf("%q classified %s, but the same option on the command line (%q) is %s", tc.env, got, tc.cli, ref)
		}
	}
}

// A command-table binding is recognised behind the builtin/command prefixes,
// inside functions, groups and shell payloads, with bundled or attached -p
// arguments, in zsh's NAME=FILE form, and for a use behind `command`.
func TestHashBindingVariantsRebindName(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, cmd := range []string{
		"builtin hash -p ./p ls; ls",
		"command hash -p ./p ls; ls",
		"f() { hash -p ./p ls; }; f; ls",
		"if true; then hash -p ./p ls; fi; ls",
		"hash ls=./p; ls",
		"hash -rp ./p ls; ls",
		"hash -p./p ls; ls",
		"bash -c 'hash -p ./p ls; ls'",
		"hash -p ./p ls; echo x | ls",
		"hash -p ./p ls; time ls",
		"hash -p ./p ls; command ls",
		"hash -p ./p ls; command -p ls",
		"(hash -p ./p ls; ls)",
		"hash -p ./p ls && ls",
		"hash -p ./p $N; ls",
		"hash -p \"$(command -v rm)\" ls; command ls -rf ~",
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("%q classified %s; the later name runs the bound program", cmd, got)
		}
	}
}

// Ordinary uses of hash, and a binding that is never used, keep their verdict.
func TestHashBindingBenignVerdictsUnchanged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, cmd := range []string{
		"hash -r", "hash -r; ls", "hash ls", "hash -l", "hash -t ls", "hash -d ls",
		"hash -p ./p ls", "hash -p ./p ls; echo hi", "hash -p ./p ls; env ls",
		"time make", "env FOO=1 go test",
	} {
		want := Safe
		switch cmd {
		case "time make", "env FOO=1 go test":
			want = CodeExecution
		}
		if got := Classify(cmd); got != want {
			t.Errorf("%q classified %s, want %s", cmd, got, want)
		}
	}
}

// The option-carrying variables are judged through env, export and declare -x,
// and a value that cannot be resolved statically is not auto-allowed.
func TestToolOptionEnvVariantsEscalate(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, cmd := range []string{
		"env TAR_OPTIONS=--to-command=./evil tar xf a.tar",
		"declare -x TAR_OPTIONS=--to-command=./evil; tar xf a.tar",
		"export ZIPOPT=-m; zip a.zip /home/user/project",
		"nice ZIPOPT=-m zip a.zip x",
		"TAR_OPTIONS=$OPTS tar xf a.tar",
		"ZIPOPT=\"$(cat o)\" zip a.zip x",
		"MAVEN_OPTS=-agentlib:jdwp=transport=dt_socket mvn -v",
		"JAVA_OPTS=@argfile java -version",
		"GRADLE_OPTS='-Xmx2g -javaagent:./e.jar' gradle -v",
		"SBT_OPTS=-Djava.system.class.loader=Evil sbt -version",
		"export MAVEN_OPTS=-Xbootclasspath/a:./e.jar",
	} {
		if got := Classify(cmd); Rank(got) <= Rank(Classify("true")) || (&DangerousConfig{}).ActionForCommand(cmd) == Allow {
			t.Errorf("%q classified %s; want it escalated and not auto-allowed", cmd, got)
		}
	}
}

// Plain option values keep the verdict of the option-free command.
func TestToolOptionEnvPlainValuesUnchanged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, cmd := range []string{
		"TAR_OPTIONS=-v echo hi",
		"TAR_OPTIONS=--warning=no-timestamp tar xf a.tar",
		"ZIPOPT=-q zip a.zip x",
		"MAVEN_OPTS=-Xmx2g echo hi",
		"MAVEN_OPTS='-Xmx2g -Dfile.encoding=UTF-8 -XX:+UseG1GC' mvn -v",
		"export TAR_OPTIONS=-v",
		"GRADLE_OPTS=-Xmx512m echo hi",
	} {
		plain := cmd
		for _, prefix := range []string{"TAR_OPTIONS=-v ", "TAR_OPTIONS=--warning=no-timestamp ", "ZIPOPT=-q ", "MAVEN_OPTS='-Xmx2g -Dfile.encoding=UTF-8 -XX:+UseG1GC' ", "MAVEN_OPTS=-Xmx2g ", "GRADLE_OPTS=-Xmx512m "} {
			plain = strings.TrimPrefix(plain, prefix)
		}
		if cmd == "export TAR_OPTIONS=-v" {
			plain = "export X=1"
		}
		if got, want := Classify(cmd), Classify(plain); got != want {
			t.Errorf("%q classified %s, want %s (the verdict of %q)", cmd, got, want, plain)
		}
	}
}

// Wrappers whose fixed operands precede the program are skipped by the nested
// gate, so the program behind them is found.
func TestNestedGateSkipsWrapperOperands(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for name, script := range map[string]string{
		"taskset":      "#!/bin/sh\ntaskset 1 ./code_policies\n",
		"chrt":         "#!/bin/sh\nchrt 10 ./code_policies\n",
		"time keyword": "#!/bin/bash\ntime ./code_policies\n",
		"nohup path":   "#!/bin/sh\n/usr/bin/nohup \"./code_policies\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := nestedFixture(t, script)
			RecordRead(filepath.Join(dir, "security.sh"))
			cls, targets := ClassifyScriptGate("bash security.sh")
			if cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
				t.Errorf("class=%s targets=%v; want unread_exec naming code_policies", cls, targets)
			}
		})
	}
}

// Well-formed quoting is followed exactly: the parse stays complete, the
// commands after the construct are seen, and a malformed or ambiguous
// construct fails the parse closed.
func TestShellScanQuotedConstructsStayInSync(t *testing.T) {
	for name, tc := range map[string]struct {
		text     string
		complete bool
		last     string
	}{
		"single-quoted default":  {"echo ${x:-'a b'} ; ./p\n", true, "./p"},
		"quoted brace":           {"echo ${x:-'}'} ; ./p\n", true, "./p"},
		"double-quoted brace":    {"echo ${x:-\"}\"} ; ./p\n", true, "./p"},
		"escaped brace":          {"echo ${x:-\\}} ; ./p\n", true, "./p"},
		"ansi-c plain":           {"echo $'a\\nb' ; ./p\n", true, "./p"},
		"ansi-c escaped quote":   {"echo $'\\'' ; ./p\n", true, "./p"},
		"ansi-c in brace":        {"echo ${x:-$'\\''} ; ./p\n", true, "./p"},
		"quoted close bracket":   {"[[ $x == \"]]\" ]]; ./p\n", true, "./p"},
		"ansi-c in bracket":      {"[[ $x == $'\\']]' ]]; ./p\n", true, "./p"},
		"escaped in bracket":     {"[[ $x == \\]] ]]; ./p\n", true, "./p"},
		"double-quoted in brace": {"echo \"${x:-a}\" ; ./p\n", true, "./p"},
		"unterminated quote":     {"echo 'abc\n./p\n", false, ""},
		"unterminated ansi-c":    {"echo $'abc\n./p\n", false, ""},
		"unterminated brace":     {"echo ${x:-abc\n./p\n", false, ""},
		"unterminated bracket":   {"[[ $x == a\n./p\n", false, ""},
		"unterminated bracket q": {"[[ $x == $'a\n./p\n", false, ""},
		"ambiguous single quote": {"echo \"${x:-'}'}\" ; ./p\n", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			cmds, complete := parseShellCommands(tc.text)
			if complete != tc.complete {
				t.Fatalf("complete=%v, want %v (commands %v)", complete, tc.complete, cmds)
			}
			if tc.last != "" && (len(cmds) == 0 || cmds[len(cmds)-1].words[0] != tc.last) {
				t.Errorf("commands %v; want the last to be %s", cmds, tc.last)
			}
		})
	}
}
