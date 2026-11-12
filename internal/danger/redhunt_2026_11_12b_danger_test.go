package danger

import (
	"path/filepath"
	"strings"
	"testing"
)

// `hash -p FILE NAME` binds NAME in the shell's command table, and every
// builtin that looks its operand up as a command reads that table: `exec
// NAME`, `command exec NAME`, `exec command NAME`, `builtin command NAME`
// all run FILE in bash. The binding was only resolved for a bare NAME or
// directly behind `command`, so the same binding used behind exec or a
// chain of lookup builtins classified as the bound name (safe).
//
// Correct behaviour: every position where the shell itself looks a word up
// as a command resolves the binding; external wrappers (env, nohup, nice,
// xargs), which search PATH on their own, do not.
func TestRED_HashBindingResolvedBehindLookupBuiltinChains(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		"hash -p ./p ls; exec ls",
		"hash -p ./p ls; builtin command ls",
		"hash -p ./p ls; command exec ls",
		"hash -p ./p ls; exec command ls",
		"hash -p ./p ls; exec -c ls",
		"hash -p ./p ls; exec -l ls",
		"hash -p ./p ls; exec -cl -- ls",
		"hash -p ./p ls; exec -a name ls",
		"hash -p ./p ls; builtin exec ls",
		"hash -p ./p ls; builtin builtin exec -c ls",
		"hash -p ./p ls; command -- exec -- ls",
		"hash -p ./p ls; command -p exec ls",
		"hash -p ./p ls; X=1 exec ls",
		"hash -p ./p ls; time command exec ls",
		"hash -p ./p ls; ! exec ls",
		"hash -p ./p ls; exec builtin command ls -la",
		"hash ls=./p; exec ls",
		"f() { hash -p ./p ls; }; f; exec command ls",
		"hash -p \"$(command -v rm)\" ls; exec ls -rf ~",
	} {
		if got, action := Classify(cmd), cfg.ActionForCommand(cmd); Rank(got) < Rank(CodeExecution) || action == Allow {
			t.Errorf("%q: class=%s action=%s; the shell looks the operand up in its command table and runs the bound file", cmd, got, action)
		}
	}
}

// External wrappers search PATH themselves, a lookup (`command -v`) runs
// nothing, and `builtin NAME` only runs builtins: none of them reads the
// command table, so their verdicts are unchanged by a binding.
func TestHashBindingExternalWrappersAndLookupsUnchanged(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ cmd, plain string }{
		{"hash -p ./p ls; nohup ls", "nohup ls"},
		{"hash -p ./p ls; env ls", "env ls"},
		{"hash -p ./p ls; nice ls", "nice ls"},
		{"hash -p ./p ls; xargs ls", "xargs ls"},
		{"hash -p ./p ls; command -v ls", "command -v ls"},
		{"hash -p ./p ls; command -pV ls", "command -pV ls"},
		{"hash -p ./p ls; builtin ls", "builtin ls"},
		{"hash -p ./p ls; exec echo ls", "exec echo ls"},
		{"hash -p ./p ls; exec command echo ls", "exec command echo ls"},
		{"exec ls", "ls"},
		{"exec command ls", "ls"},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); got != want {
			t.Errorf("%q classified %s, want %s (the verdict of %q)", tc.cmd, got, want, tc.plain)
		}
	}
}

// An option-carrying variable (TAR_OPTIONS, UNZIPOPT, MAVEN_ARGS, ...) was
// judged by classifying the tool's bare stage with the words added, which
// sees exec and delete options but not the path targets the full analysis
// resolves: `TAR_OPTIONS=-C/etc tar xf a.tar` extracts into /etc yet stayed
// local_write while `tar xf a.tar -C/etc` is system_write. MAVEN_ARGS (Maven
// 3.9+ reads it as extra command-line arguments: -s, -f, -D...) was not
// known at all.
//
// Correct behaviour: the invocation is analysed with the variable's words
// spliced into the argv where the tool reads them, so every option class the
// per-tool adapters know (exec, delete, path target) is judged exactly like
// the same option written out.
func TestRED_ToolOptionEnvSplicedIntoTheToolArgv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ env, cli string }{
		{"TAR_OPTIONS=--directory=/etc tar xf a.tar", "tar --directory=/etc xf a.tar"},
		{"TAR_OPTIONS=-C/etc tar xf a.tar", "tar xf a.tar -C/etc"},
		{"TAR_OPTIONS='-C /etc' gtar xf a.tar", "gtar xf a.tar -C /etc"},
		{"UNZIPOPT=-d/etc unzip a.zip", "unzip -d/etc a.zip"},
		{"UNZIPOPT='-d /etc' unzip a.zip", "unzip -d /etc a.zip"},
		{"UNZIP=-d/etc unzip a.zip", "unzip -d/etc a.zip"},
		{"env TAR_OPTIONS=-C/etc tar xf a.tar", "tar xf a.tar -C/etc"},
		{"TAR_OPTIONS=-C/etc nice tar xf a.tar", "tar xf a.tar -C/etc"},
		{"export TAR_OPTIONS=-C/etc; tar xf a.tar", "tar xf a.tar -C/etc"},
		{"export UNZIPOPT=-d/etc && unzip a.zip", "unzip -d/etc a.zip"},
		// The inline assignment reaches the commands a shell payload starts.
		{"TAR_OPTIONS=-C/etc bash -c 'tar xf a.tar'", "bash -c 'tar xf a.tar -C/etc'"},
		{"env UNZIPOPT=-d/etc sh -c 'unzip a.zip'", "sh -c 'unzip -d/etc a.zip'"},
		{"MAVEN_ARGS='-s evil.xml' mvn -v", "mvn -s evil.xml -v"},
		{"MAVEN_ARGS=-fevil/pom.xml mvn -v", "mvn -fevil/pom.xml -v"},
		{"export MAVEN_ARGS=-Dexec.executable=./x; mvn -v", "mvn -Dexec.executable=./x -v"},
	} {
		ref, got := Classify(tc.cli), Classify(tc.env)
		if Rank(ref) <= Rank(LocalWrite) {
			t.Fatalf("reference %q classified %s; test premise broken", tc.cli, ref)
		}
		if Rank(got) < Rank(ref) {
			t.Errorf("%q classified %s, but the same option on the command line (%q) is %s", tc.env, got, tc.cli, ref)
		}
	}
}

// A variable assigned by one statement and exported by another (or by set
// -a, or before the assignment) reaches the tool just like an inline one:
// `declare TAR_OPTIONS=--to-command=./evil; export TAR_OPTIONS; tar xf a.tar`
// runs ./evil for every member, yet classified local_write because only
// inline assignments and `export`/`declare -x NAME=value` were judged.
//
// Correct behaviour: a value recorded by any assignment form is in effect for
// a later run of the tool and is spliced into its argv.
func TestRED_ToolOptionEnvAssignedThenExported(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ env, cli string }{
		{"declare TAR_OPTIONS=--to-command=./evil; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar --to-command=./evil"},
		{"typeset TAR_OPTIONS=-C/etc; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar -C/etc"},
		{"f() { local TAR_OPTIONS=--to-command=./evil; export TAR_OPTIONS; tar xf a.tar; }; f", "tar xf a.tar --to-command=./evil"},
		{"readonly TAR_OPTIONS=--to-command=./evil; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar --to-command=./evil"},
		{"declare -g TAR_OPTIONS=--to-command=./evil; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar --to-command=./evil"},
		{"declare -g TAR_OPTIONS=-C/etc; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar -C/etc"},
		{"export TAR_OPTIONS; TAR_OPTIONS=-C/etc; tar xf a.tar", "tar xf a.tar -C/etc"},
		{"set -a; TAR_OPTIONS=-C/etc; tar xf a.tar", "tar xf a.tar -C/etc"},
		{"UNZIPOPT=-d/etc; export UNZIPOPT; unzip a.zip", "unzip -d/etc a.zip"},
		{"declare ZIPOPT=-m; export ZIPOPT; zip a.zip /home/user/project", "zip -m a.zip /home/user/project"},
		{"declare MAVEN_ARGS='-s evil.xml'; export MAVEN_ARGS; mvn -v", "mvn -s evil.xml -v"},
		{"declare MAVEN_OPTS=-javaagent:./e.jar; export MAVEN_OPTS; mvn -v", "JAVA_TOOL_OPTIONS=-javaagent:./e.jar mvn -v"},
		{"readonly GRADLE_OPTS=-javaagent:./e.jar; export GRADLE_OPTS; gradle -v", "JAVA_TOOL_OPTIONS=-javaagent:./e.jar gradle -v"},
	} {
		ref, got := Classify(tc.cli), Classify(tc.env)
		if Rank(ref) <= Rank(LocalWrite) {
			t.Fatalf("reference %q classified %s; test premise broken", tc.cli, ref)
		}
		if Rank(got) < Rank(ref) {
			t.Errorf("%q classified %s, but the same option on the command line (%q) is %s", tc.env, got, tc.cli, ref)
		}
	}
}

// When the command line binds an option-carrying variable in a way whose
// value or export status the analysis cannot follow (a branch, read, a
// nameref, a dynamically named declaration, eval), a later run of the tool
// that reads it fails closed.
func TestRED_ToolOptionEnvUndeterminedValueFailsClosed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, cmd := range []string{
		"[ -f x ] && TAR_OPTIONS=-C/etc; tar xf a.tar",
		"if [ -f x ]; then TAR_OPTIONS=-C/etc; fi; export TAR_OPTIONS; tar xf a.tar",
		"read TAR_OPTIONS; export TAR_OPTIONS; tar xf a.tar",
		"declare \"$x=-C/etc\"; export \"$x\"; tar xf a.tar",
		"declare -n r=TAR_OPTIONS; r=-C/etc; export TAR_OPTIONS; tar xf a.tar",
		"TAR_OPTIONS=$(cat o); export TAR_OPTIONS; tar xf a.tar",
		"eval 'TAR_OPTIONS=-C/etc'; export TAR_OPTIONS; tar xf a.tar",
		"printf -v TAR_OPTIONS %s -C/etc; export TAR_OPTIONS; tar xf a.tar",
		": ${UNZIPOPT:=-d/etc}; export UNZIPOPT; unzip a.zip",
		"read MAVEN_ARGS; export MAVEN_ARGS; mvn -v",
	} {
		if got, action := Classify(cmd), cfg.ActionForCommand(cmd); Rank(got) < Rank(SystemWrite) || action == Allow {
			t.Errorf("%q: class=%s action=%s; the tool reads a variable whose value cannot be determined", cmd, got, action)
		}
	}
}

// Ordinary option values, and the common JVM tuning flags, keep the verdict
// of the option-free command; a file name that merely contains a variable's
// name does not count as binding it.
func TestToolOptionEnvBenignValuesKeepVerdict(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ cmd, plain string }{
		{"TAR_OPTIONS=-v tar xf a.tar", "tar xf a.tar"},
		{"export TAR_OPTIONS=-v; tar xf a.tar", "tar xf a.tar"},
		{"declare TAR_OPTIONS=--warning=no-timestamp; export TAR_OPTIONS; tar xf a.tar", "tar xf a.tar"},
		{"UNZIPOPT=-q unzip a.zip", "unzip a.zip"},
		{"unzip ARCHIVE.ZIP", "unzip a.zip"},
		{"zip BACKUP_ZIP.zip x", "zip a.zip x"},
		{"echo $TAR_OPTIONS", "echo hi"},
		{"TAR_OPTIONS=-C/etc echo hi; tar xf a.tar", "tar xf a.tar"},
		{"unset TAR_OPTIONS; tar xf a.tar", "tar xf a.tar"},
		{"read -p \"$msg\" ans; tar xf a.tar", "read -r ans; tar xf a.tar"},
		{"declare stamp=$(date); tar xf a.tar", "tar xf a.tar"},
		{"printf -v out %s hi; tar xf a.tar", "tar xf a.tar"},
		{"MAVEN_OPTS=-Xmx2g mvn test", "mvn test"},
		{"MAVEN_ARGS=-B mvn test", "mvn test"},
		{"env FOO=1 go test", "go test"},
		{"MAVEN_OPTS=-Dfile.encoding=UTF-8 mvn -v", "mvn -v"},
		{"MAVEN_OPTS=-Djava.awt.headless=true mvn -v", "mvn -v"},
		{"MAVEN_OPTS=-XX:+UseG1GC mvn -v", "mvn -v"},
		{"MAVEN_OPTS=-Duser.timezone=Europe/Berlin mvn -v", "mvn -v"},
		{"MAVEN_OPTS=-Dmaven.repo.local=.m2 mvn -v", "mvn -v"},
		{"JAVA_OPTS='-Dfile.encoding=UTF-8 -Djava.awt.headless=true -XX:+UseG1GC' java -version", "java -version"},
		{"export JAVA_OPTS='-Xmx1g -Duser.timezone=Europe/Berlin'; gradle -v", "gradle -v"},
		{"declare MAVEN_OPTS=-Xmx2g; export MAVEN_OPTS; mvn -v", "mvn -v"},
	} {
		if got, want := Classify(tc.cmd), Classify(tc.plain); got != want {
			t.Errorf("%q classified %s, want %s (the verdict of %q)", tc.cmd, got, want, tc.plain)
		}
	}
	for cmd, want := range map[string]RiskClass{
		"TAR_OPTIONS=-v tar xf a.tar": LocalWrite,
		"MAVEN_OPTS=-Xmx2g mvn test":  CodeExecution,
		"env FOO=1 go test":           CodeExecution,
	} {
		if got := Classify(cmd); got != want {
			t.Errorf("%q classified %s, want %s", cmd, got, want)
		}
	}
}

// The nested-script scanner followed a substitution inside a ${...} default
// or a here-document body by reading the rest of a whole shell word after
// it, so the double quote closing "$(cmd)" was taken as an opening quote,
// the text ended "inside a quote" and the parse failed closed: scripts that
// launch nothing local gated as unread_exec with the script as the target.
// In an unquoted here-document body quotes are ordinary characters; only a
// backslash and the expansions are active.
//
// Correct behaviour: these scripts parse completely and launch nothing, so a
// read script stays licensed.
func TestRED_NestedScannerQuotedSubstitutionDoesNotFailClosed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for name, script := range map[string]string{
		"quoted subst in default":      "#!/bin/bash\nV=${VER:-\"$(echo dev)\"}\n",
		"quoted subst in := default":   "#!/bin/bash\n: \"${OUT:=\"$(pwd)/out\"}\"\n",
		"heredoc quoted subst":         "#!/bin/bash\ncat <<EOF\nversion: \"$(git describe)\"\nEOF\n",
		"heredoc apostrophe and subst": "#!/bin/bash\ncat <<EOF\nDear $USER, \"$(hostname)\" says: it's alive\nEOF\n",
		"heredoc single-quoted subst":  "#!/bin/bash\ncat <<EOF\nname='$(whoami)'\nEOF\n",
		"heredoc backtick in quotes":   "#!/bin/bash\ncat <<EOF\ndate: \"`date`\"\nEOF\n",
		"heredoc braced default":       "#!/bin/bash\ncat <<-EOF\n\tpath: \"${PREFIX:-/usr/local}\"\nEOF\n",
		"heredoc arithmetic in quotes": "#!/bin/bash\ncat <<EOF\nn: \"$((1 + 2))\"\nEOF\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, complete := parseShellCommands(script); !complete {
				t.Errorf("script %q: parse failed closed; it is well formed", script)
			}
			d := nestedFixture(t, script)
			RecordRead(filepath.Join(d, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
				t.Errorf("script %q: class=%s targets=%v; the script launches nothing local", script, cls, targets)
			}
		})
	}
}

// A launch placed after each construct the scanner has to read quote-aware is
// still found, or the parse fails closed: the quote-context fix must not let
// a construct swallow what follows it.
func TestNestedScannerLaunchAfterTrickyConstructStaysGated(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for name, construct := range map[string]string{
		"double-quoted quote in default": `echo ${x:-"'"}`,
		"escaped brace in default":       `echo ${x:-\}}`,
		"quoted ]] in [[ ]]":             `[[ $x == "]]" ]]`,
		"quoted ]] after =~":             `[[ $x =~ "a]]" ]]`,
		"locale string apostrophe":       `echo $"it's"`,
		"ansi-c escaped quote":           `echo $'a\''`,
		"case pattern parenthesis":       "case $x in a\\)|\"b)\") echo hit;; esac",
		"backtick with quote":            "echo `echo \"'\"`",
		"comment with quote":             "# it's a comment \"",
		"line continuation in quotes":    "echo \"a \\\nb\"",
		"arithmetic with quotes":         `echo $(( '1' ))`,
		"array literal parenthesis":      `arr=(a "b)" c)`,
		"heredoc inside substitution":    "x=$(cat <<EOF\n'\nEOF\n)",
		"quoted subst in default":        `V=${VER:-"$(echo dev)"}`,
		"heredoc quoted subst":           "cat <<EOF\nversion: \"$(git describe)\"\nEOF",
		"heredoc apostrophe":             "cat <<EOF\nit's \"$(hostname)\"\nEOF",
		"quoted heredoc":                 "cat <<'EOF'\n$(\"'\nEOF",
		"subshell in arithmetic form":    `echo $( (echo "'") )`,
	} {
		t.Run(name, func(t *testing.T) {
			script := "#!/bin/bash\n" + construct + "\n./code_policies\n"
			cmds, complete := parseShellCommands(script)
			if complete && (len(cmds) == 0 || cmds[len(cmds)-1].words[0] != "./code_policies") {
				t.Errorf("script %q: parse complete but the last command is not the launch (%v)", script, cmds)
			}
			d := nestedFixture(t, script)
			RecordRead(filepath.Join(d, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec || len(targets) == 0 {
				t.Errorf("script %q: class=%s targets=%v; want unread_exec (the launch after the construct runs)", script, cls, targets)
			}
		})
	}
}

// `$((cmd) )` is a command substitution holding a subshell, not arithmetic:
// bash only reads `$((` as arithmetic when the inner group closes right
// before the outer one. The command inside runs.
func TestNestedScannerSubshellSubstitutionIsNotArithmetic(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, script := range []string{
		"#!/bin/bash\nx=$((./code_policies) )\n",
		"#!/bin/bash\necho \"$((./code_policies) | tr a b)\"\n",
	} {
		d := nestedFixture(t, script)
		RecordRead(filepath.Join(d, "security.sh"))
		if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
			t.Errorf("script %q: class=%s targets=%v; want unread_exec naming code_policies", script, cls, targets)
		}
	}
}

// Realistic scripts — an installer with here-docs and ${VAR:-"$(cmd)"}
// defaults, a CI script, a build script — parse completely and launch
// nothing local, so a read copy stays licensed; the same script with a
// launch appended is gated.
func TestNestedScannerRealisticScriptsCorpus(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	corpus := map[string]string{
		"install": `#!/usr/bin/env bash
set -euo pipefail
# Installer: it's safe to re-run.
VERSION=${VERSION:-"$(curl -fsSL https://example.com/latest | tr -d 'v')"}
PREFIX="${PREFIX:-"$HOME/.local"}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cat > "$tmp/config.yaml" <<EOF
# generated by the installer on $(date -u +%Y-%m-%dT%H:%M:%SZ)
version: "${VERSION}"
prefix: "$PREFIX"
user: "$(id -un)"
note: it's "quoted" here
EOF
cat <<'USAGE'
Usage: tool [--help] "$(not run)"
USAGE
mkdir -p "$PREFIX/bin"
echo "installed ${VERSION} for ${OS}/${ARCH} into ${PREFIX}"
`,
		"ci": `#!/bin/sh
set -e
: "${GOFLAGS:=-mod=readonly}"
: "${CI_COMMIT_SHA:="$(git rev-parse HEAD)"}"
echo "commit: ${CI_COMMIT_SHA:-unknown}"
if [ -n "${CI:-}" ] && [ "$(git status --porcelain | wc -l)" -gt 0 ]; then
  echo "tree is dirty" >&2
  exit 1
fi
go vet ./...
go test -race -count=1 ./... 2>&1 | tee "test-$(date +%s).log"
cat <<EOF >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
### Tests passed on $(go version) ("$(uname -s)", it's green)
EOF
`,
		"build": `#!/bin/bash
NAME=${NAME:-"$(basename "$(pwd)")"}
LDFLAGS="-s -w -X main.version=${VERSION:-"$(git describe --tags --always 2>/dev/null || echo dev)"}"
targets=(linux/amd64 "darwin/arm64" windows/amd64)
for t in "${targets[@]}"; do
  os="${t%%/*}"; arch="${t##*/}"
  out="dist/${NAME}_${os}_${arch}$([[ $os == windows ]] && echo .exe)"
  GOOS=$os GOARCH=$arch go build -ldflags "$LDFLAGS" -o "$out" .
done
[[ -d dist ]] && ls -la dist
echo "built $(( ${#targets[@]} )) targets"
`,
	}
	for name, script := range corpus {
		t.Run(name, func(t *testing.T) {
			if _, complete := parseShellCommands(script); !complete {
				t.Fatalf("parse failed closed on a well-formed script")
			}
			d := nestedFixture(t, script)
			RecordRead(filepath.Join(d, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls == UnreadExec {
				t.Errorf("class=%s targets=%v; the script launches nothing local", cls, targets)
			}
			launched := script + "./code_policies --scan\n"
			d = nestedFixture(t, launched)
			RecordRead(filepath.Join(d, "security.sh"))
			if cls, targets := ClassifyScriptGate("bash security.sh"); cls != UnreadExec || !strings.Contains(targetNames(targets), "code_policies") {
				t.Errorf("with a launch appended: class=%s targets=%v; want unread_exec naming code_policies", cls, targets)
			}
		})
	}
}
