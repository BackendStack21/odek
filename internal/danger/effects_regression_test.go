package danger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectsConfirmedBypasses(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    RiskClass
	}{
		{"novelverb > /dev/null", Unknown},
		{"X=rm; $X -rf / > /dev/null", Destructive},
		{"D=/; rm -rf \"$D\"", Destructive},
		{"cd /etc; touch odek-audit", SystemWrite},
		{"env -C /etc touch odek-audit", SystemWrite},
		{"awk 'BEGIN { system (\"id\") }'", CodeExecution},
		{"awk 'BEGIN {print \"x\" > \".envrc\"}'", Unknown},
		{"sed '1e id' ./input", CodeExecution},
		{"sed '1{s/x/id/e;}' ./input", CodeExecution},
		{"sed 'w .envrc' ./input", Persistence},
		{"sed 'r ./extra-input' ./input", Unknown},
		{"rg --pre ./helper pattern ./input", CodeExecution},
		{"fd --exec ./helper", CodeExecution},
		{"sqlite3 :memory: '.read ./commands.sql'", CodeExecution},
		{"sqlite3 :memory: '.load ./extension'", CodeExecution},
		{"sqlite3 :memory: '.output /etc/odek-audit' 'select 1;'", SystemWrite},
		{"tar --checkpoint=1 --checkpoint-action=exec=id -cf out.tar ./input", CodeExecution},
		{"tar -Iid -cf out.tar ./input", CodeExecution},
		{"curl -o .envrc https://example.com", Persistence},
		{"curl -o /etc/odek-audit https://example.com", SystemWrite},
		{"wget -O .git/hooks/pre-commit https://example.com", Persistence},
		{"git -c core.hooksPath=./hooks commit -m audit", CodeExecution},
		{"git diff --ext-diff", CodeExecution},
		{"git status", CodeExecution},
		{"git add input", CodeExecution},
		{"curl -K ./settings", CodeExecution},
		{"wget --config=./settings", Unknown},
		{"tar --checkpoint-action exec=id -cf out input", CodeExecution},
		{"go test ./...", CodeExecution},
		{"go build -toolexec=./helper ./...", CodeExecution},
		{"cargo build", CodeExecution},
		{"ninja", CodeExecution},
		{"cmake -P ./program.cmake", CodeExecution},
		{"gcc -fplugin=./plugin.so ./input.c", CodeExecution},
		{"protoc --plugin=protoc-gen-audit=./helper --audit_out=. ./input.proto", CodeExecution},
		{"node --check --require ./preload.js ./main.js", CodeExecution},
		{"hostname changed-name", SystemWrite},
		{"ip link set dev lo down", SystemWrite},
		{"sysctl kernel.randomize_va_space=0", SystemWrite},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := Classify(tc.command); got != tc.want {
				t.Fatalf("got %s; want %s; effects=%v", got, tc.want, Analyze(tc.command).Effects)
			}
		})
	}
}

func TestEffectsOutputOptionSpellings(t *testing.T) {
	for _, command := range []string{
		"curl -o/etc/audit https://example.com", "curl -so/etc/audit https://example.com", "curl -sSo /etc/audit https://example.com", "curl --output=/etc/audit https://example.com",
		"curl --dump-header /etc/audit https://example.com", "curl -D/etc/audit https://example.com", "curl --cookie-jar /etc/audit https://example.com", "curl --trace-ascii /etc/audit https://example.com",
		"curl --output-dir /etc -o audit https://example.com", "curl --output-dir=/etc -o audit https://example.com", "curl -O https://example.com/.envrc", "curl --remote-name-all https://example.com/.envrc", "curl -o", "curl -O", "curl -so", "gofmt -w .envrc", "gcc -o/etc/audit input.c",
		"wget --output-document=/etc/audit https://example.com", "wget -qO/etc/audit https://example.com", "wget --output-file=/etc/audit https://example.com", "wget --append-output /etc/audit https://example.com",
		"wget -P/etc https://example.com/audit", "wget --directory-prefix=/etc https://example.com/audit", "wget --directory-prefix /etc https://example.com/audit", "wget -O", "wget -P /etc -O audit https://example.com/audit",
		"sort -o/etc/audit input", "sort --output /etc/audit input", "gpg --output /etc/audit --decrypt input", "gpg2 -o/etc/audit --decrypt input", "find . -fprint0 /etc/audit", "find . -fprintf /etc/audit '%p'",
		"cp -t/etc input", "mv --target-directory=/etc input", "install -t /etc input", "xxd -r input /etc/audit",
		"sed -nEi 's/x/y/' .envrc", "sed --in-place=.bak 's/x/y/' .envrc", "sed '1w .envrc' input", "sed -e'w .envrc' input", "sed --expression='w .envrc' input", "sed -ne's/x/y/w .envrc' input", "sed 's/x/y/w .envrc' input", "sed '1{s#x#y#w /etc/audit;}' input", "sqlite3 :memory: '.once .envrc' 'select 1;'", "sqlite3 :memory: '.save .envrc'", "sqlite3 :memory: '.backup .envrc'",
	} {
		t.Run(command, func(t *testing.T) {
			if got := Classify(command); Rank(got) < Rank(SystemWrite) {
				t.Errorf("unprotected destination: %s (%v)", got, Analyze(command).Effects)
			}
		})
	}
	for _, command := range []string{"curl -XPOST -o output https://example.com", "curl -Tinput https://example.com", "sort -k2 input", "gpg -rpublic --encrypt input", "install -m755 input output"} {
		if got := Classify(command); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("option value became a destination: %s: %s", command, got)
		}
	}
}

func TestEffectsNestedPayloadsAndApprovalClass(t *testing.T) {
	cfg := DangerousConfig{Classes: map[RiskClass]Action{NetworkEgress: Deny, CodeExecution: Allow}}
	for _, command := range []string{"sh -c 'curl https://example.com'", "env sh -c 'curl https://example.com; node -e 0'", "eval 'curl https://example.com'", "git submodule foreach 'curl https://example.com'", "echo $(curl https://example.com)"} {
		if cfg.ActionForCommand(command) != Deny {
			t.Errorf("nested egress escaped: %s", command)
		}
	}
	for _, tc := range []struct {
		cfg  DangerousConfig
		cmd  string
		want RiskClass
	}{
		{DangerousConfig{}, "echo ok", Safe},
		{DangerousConfig{}, "node -e 0", CodeExecution},
		{DangerousConfig{Classes: map[RiskClass]Action{LocalWrite: Prompt, CodeExecution: Allow}}, "node -e 0 > output", LocalWrite},
		{DangerousConfig{Classes: map[RiskClass]Action{LocalWrite: Prompt}}, "node -e 0 > output", ToolBatchClass},
	} {
		if got := tc.cfg.PromptClassForCommand(tc.cmd); got != tc.want {
			t.Errorf("%s: prompt=%s want=%s", tc.cmd, got, tc.want)
		}
	}
	if got := analyzeAtDepth("echo ok", maxSubstDepth+1).Class(); got != Unknown {
		t.Fatal("deep recursion did not fail closed")
	}
}

func TestEffectsStateAndHelperSpellings(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("child", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("child/helper", []byte("echo ok\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"cd child; rg --pre ./helper pattern file", "env --chdir=child rg --pre ./helper pattern file", "env -Cchild sh -c './helper'", "cd child; sh -c './helper'"} {
		if targets := UnreadScriptTargets(command); len(targets) != 1 || filepath.Base(targets[0]) != "helper" {
			t.Errorf("%s: missing cwd-aware target: %v", command, targets)
		}
	}
	for _, command := range []string{"env -C", "env --chdir=$UNSET_UNKNOWN touch file", "env -C missing touch file", "cd -; touch file", "cd missing; touch file", "popd; touch file", "cd child || touch file", "cd child & touch file", "cd child; echo $(cat file)"} {
		if Classify(command) != Unknown && command != "env -C" {
			t.Errorf("uncertain state allowed: %s: %s", command, Classify(command))
		}
	}
	for _, command := range []string{"cd /etc; printf x > 1", "env -C /etc printf x > -", "env -u UNUSED --chdir /etc touch file", "env -C /etc env -C /etc touch file"} {
		if got := Classify(command); got != SystemWrite {
			t.Errorf("%s: %s", command, got)
		}
	}
	if Classify("echo env -C /etc > file") != LocalWrite {
		t.Fatal("display text changed execution directory")
	}
	for _, command := range []string{"env -- touch file", "cd child | cat; touch file", "env -u UNUSED -- touch file"} {
		if got := Classify(command); got != LocalWrite {
			t.Errorf("wrapper/pipeline state changed ordinary write: %s: %s", command, got)
		}
	}
	if got := Classify("A=/; sh -c 'rm -rf $A'"); got != Destructive {
		t.Errorf("nested payload lost assigned variable: %s", got)
	}
	for _, command := range []string{"A=.; AA=/; rm -rf $AA", "A=/; rm -rf ${A}", "A=/; rm -rf $A"} {
		if Classify(command) != Destructive {
			t.Errorf("assignment expansion escaped: %s", command)
		}
	}
	if Classify("cd child; printf ok > /dev/null") != LocalWrite {
		t.Fatal("absolute discard became uncertain")
	}
	for _, command := range []string{"fd -x./child/helper", "tar -I./child/helper -cf out input", "tar --checkpoint-action=exec=./child/helper -cf out input", "gcc -fplugin=./child/helper input", "go build -toolexec=./child/helper .", "protoc --plugin=protoc-gen-audit=./child/helper input", "gawk --load=./child/helper 'BEGIN{print 1}'"} {
		if targets := UnreadScriptTargets(command); len(targets) == 0 {
			t.Errorf("helper operand escaped: %s", command)
		}
	}
	for _, command := range []string{"fd --exec", "tar -I", "protoc --plugin=./child/helper input", "node --require=./child/helper main", "node -r./child/helper main"} {
		if got := Classify(command); got != CodeExecution {
			t.Errorf("helper invocation lost execution risk: %s: %s", command, got)
		}
		want := 1
		if command == "fd --exec" || command == "tar -I" {
			want = 0
		}
		if got := len(UnreadScriptTargets(command)); got != want {
			t.Errorf("%s: unread targets=%d want=%d", command, got, want)
		}
	}
	if executableTextFile("missing") {
		t.Fatal("missing file treated as executable text")
	}
	if err := os.WriteFile("binary", []byte{0x7f, 'E', 'L', 'F', 0}, 0700); err != nil {
		t.Fatal(err)
	}
	if executableTextFile("binary") {
		t.Fatal("binary treated as shell text")
	}
	if err := os.WriteFile("empty", nil, 0700); err != nil {
		t.Fatal(err)
	}
	if executableTextFile("empty") {
		t.Fatal("empty file treated as shell text")
	}
	if err := os.Symlink("cycle", "cycle"); err != nil {
		t.Fatal(err)
	}
	if got := Classify("touch cycle"); Rank(got) < Rank(SystemWrite) {
		t.Errorf("failed resolution did not gate: %s", got)
	}
	if Classify("env PATH=./child cat") != SystemWrite {
		t.Fatal("PATH hijack not gated")
	}
	for _, assignment := range []string{"A=$A$A;", "A=${A}${A};"} {
		if got := Classify("A=x;" + strings.Repeat(assignment, 40) + "touch $A"); got != Unknown {
			t.Errorf("exponential static expansion did not fail closed: %s", got)
		}
	}
}

func TestEffectsIndependentDenials(t *testing.T) {
	for _, denied := range []RiskClass{NetworkEgress, LocalWrite, CodeExecution, Install, SystemWrite, Persistence, Destructive, Unknown} {
		cfg := DangerousConfig{DefaultAction: strPtr("allow"), Classes: map[RiskClass]Action{denied: Deny}}
		commands := map[RiskClass]string{NetworkEgress: "curl https://example.com", LocalWrite: "touch file", CodeExecution: "node -e '0'", Install: "pip install demo", SystemWrite: "cat /etc/hosts", Persistence: "printf x > .envrc", Destructive: "rm -rf /", Unknown: "novelverb"}
		for _, suffix := range []string{"", "; echo ok", "; node -e '0'", " | cat", " > /dev/null", "; novelverb"} {
			command := commands[denied] + suffix
			if got := cfg.ActionForCommand(command); got != Deny {
				t.Errorf("%s: denied %s became %s", command, denied, got)
			}
		}
	}
	for _, cfg := range []DangerousConfig{{Classes: map[RiskClass]Action{Blocked: Allow}}, {Allowlist: []string{"dd if=x of=/dev/sda bs=1M"}}} {
		if cfg.ActionForCommand("dd if=x of=/dev/sda bs=1M") != Deny || cfg.ActionFor(Blocked) != Deny {
			t.Fatal("blocked invariant overridden")
		}
	}
}

func TestEffectsExecutionDenialSurvivesProtectedOperands(t *testing.T) {
	allow := "allow"
	cfg := DangerousConfig{DefaultAction: &allow, Classes: map[RiskClass]Action{CodeExecution: Deny}}
	for _, command := range []string{"sh /etc/odek-audit-script", "find /etc -exec node -e 0 \\;", "printf data | awk '{print 1}' /etc/hosts"} {
		if got := cfg.ActionForCommand(command); got != Deny {
			t.Errorf("execution effect hidden by protected operand: %s: %s", command, got)
		}
	}
	protected := DangerousConfig{DefaultAction: &allow, Classes: map[RiskClass]Action{SystemWrite: Deny}}
	if got := protected.ActionForCommand("novelverb /etc/shadow"); got != Deny {
		t.Errorf("unknown allowance hid protected read: %s", got)
	}
}

func TestEffectsUnavailableDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if got := Classify("touch ordinary-file"); got != Unknown {
		t.Fatalf("missing cwd authorized a relative write: %s", got)
	}
}

func TestEffectsInspectionFalsePositives(t *testing.T) {
	for _, cmd := range []string{"jq '.scripts' package.json", "npm pkg get scripts", "cat /etc/passwd", "sed -n '1p' file", `sed 's/a\/b/c\/d/' file`, "tar -tf archive.tar", "unzip -l archive.zip", "wget --version", "node --check file.js"} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("%s: got %s", cmd, got)
		}
	}
	if got := Classify("wget https://example.com/"); got != NetworkEgress {
		t.Errorf("default index output changed ordinary egress: %s", got)
	}
	for _, path := range []string{"/device-audit", "/variety-audit", "/etcetera-audit"} {
		if got := ClassifyPath(path); got != LocalWrite {
			t.Errorf("%s: got %s", path, got)
		}
	}
}

func TestEffectsExecutableIdentityAndHelpers(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, name := range []string{"cat", "no-shebang", "preprocessor", "commands.sql", "preload.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("echo marker\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range []string{filepath.Join(dir, "cat"), "./cat", "./no-shebang", "rg --pre ./preprocessor pattern file", "sqlite3 :memory: '.read ./commands.sql'", "node --check --require ./preload.js file.js"} {
		_, targets := ClassifyScriptGate(cmd)
		if len(targets) == 0 {
			t.Errorf("%s: helper/target not gated", cmd)
		}
	}
	if targets := UnreadScriptTargets("node --check ./preload.js"); len(targets) > 0 {
		t.Errorf("syntax-only file should not execute: %v", targets)
	}
}
