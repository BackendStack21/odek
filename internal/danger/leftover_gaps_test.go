package danger

import (
	"strings"
	"testing"
)

func lgHas(cmd string, cls RiskClass) bool {
	for _, e := range Analyze(cmd).Effects {
		if e == cls {
			return true
		}
	}
	return false
}

// Wrappers that take value-bearing options (a signal name, a niceness, a
// user, a CPU list) must have those values consumed so the wrapped command is
// the one that gets classified. Before, `timeout -s KILL 60 rm -rf /` read the
// signal name as the command and classified unknown instead of destructive.
func TestLeftover_TransparentWrappersClassifyInnerCommand(t *testing.T) {
	prefixes := []string{
		"timeout -s KILL 60",
		"timeout -sKILL 60",
		"timeout --signal=KILL 60",
		"timeout --signal KILL 60",
		"timeout --sig KILL 60",
		"timeout -k 5 60",
		"timeout -k5 60",
		"timeout --kill-after=5 60",
		"timeout --kill-after 5 60",
		"timeout --foreground -s KILL 60",
		"stdbuf -o L",
		"stdbuf -oL",
		"stdbuf -i0 -oL -eL",
		"stdbuf --output=L",
		"stdbuf --output L",
		"nice -n 10",
		"nice -n10",
		"nice --adjustment=10",
		"ionice -c 3",
		"ionice -c3 -n 7",
		"chrt -f 1",
		"chrt --fifo 1",
		"chrt -r 10",
		"taskset -c 0",
		"taskset -c 0,1",
		"taskset 0x3",
		"flock /tmp/l",
		"flock -n /tmp/l",
		"flock -w 5 /tmp/l",
		"flock --timeout=5 /tmp/l",
		"unbuffer",
		"unbuffer -p",
		"arch -x86_64",
		"arch -arm64",
		"arch -arch x86_64",
		"asdf exec",
		"watch -n 1 -x",
		"nohup timeout -s KILL 60",
		"time nice -n 10 timeout -k 5 60",
	}
	inners := []struct {
		cmd  string
		want RiskClass
	}{
		{"ls", Safe},
		{"go test ./...", CodeExecution},
		{"rm -rf /", Destructive},
	}
	for _, p := range prefixes {
		for _, in := range inners {
			cmd := p + " " + in.cmd
			got := Classify(cmd)
			if got != in.want {
				t.Errorf("Classify(%q) = %s, want %s (same as the bare command)", cmd, got, in.want)
			}
		}
		if wrAction(p+" rm -rf /") != Deny {
			t.Errorf("ActionForCommand(%q) is not deny", p+" rm -rf /")
		}
	}
}

// Privileged wrappers keep their system_write floor and still expose the
// wrapped command when options carry values.
func TestLeftover_PrivilegedWrapperOptionValues(t *testing.T) {
	prefixes := []string{
		"sudo -u user",
		"sudo -uuser",
		"sudo --user=user",
		"sudo --user user",
		"sudo -g grp",
		"sudo -u user -g grp",
		"sudo -E",
		"sudo -E -u user",
		"sudo -H -n -u user",
		"sudo -C 5 -u user",
		"sudo -D /tmp",
		"sudo -h host",
		"sudo -p prompt",
		"sudo FOO=bar",
		"doas -u user",
		"doas -C /etc/doas.conf",
		"doas -n -u user",
	}
	for _, p := range prefixes {
		if got := Classify(p + " ls"); got != SystemWrite {
			t.Errorf("Classify(%q) = %s, want system_write (floor, nothing worse inside)", p+" ls", got)
		}
		if got := Classify(p + " rm -rf /"); got != Destructive {
			t.Errorf("Classify(%q) = %s, want destructive", p+" rm -rf /", got)
		}
		if !lgHas(p+" go test ./...", CodeExecution) {
			t.Errorf("Analyze(%q) lacks code_execution", p+" go test ./...")
		}
	}
}

// Wrappers that run their payload through a shell, or that fetch and run
// something, classify the payload and carry a code_execution floor.
func TestLeftover_ShellPayloadWrappers(t *testing.T) {
	deny := []string{
		"script -qc 'rm -rf /' /dev/null",
		"script -q -c 'rm -rf /' /dev/null",
		"script --command='rm -rf /' /dev/null",
		"script --command 'rm -rf /' /dev/null",
		"watch -n 1 'rm -rf /'",
		"watch 'rm -rf /'",
		"watch -n1 'ls; rm -rf /'",
		"watch --interval 5 'ls && rm -rf /'",
		"nix-shell --run 'rm -rf /'",
		"nix-shell -p hello --run 'rm -rf /'",
		"nix-shell --command 'rm -rf /'",
		"nix shell nixpkgs#hello -c rm -rf /",
		"nix shell nixpkgs#hello --command rm -rf /",
		"nix develop -c rm -rf /",
		"nix develop --command rm -rf /",
		"flock /tmp/l -c 'rm -rf /'",
		"flock -n /tmp/l -c 'rm -rf /'",
		"flock /tmp/l --command 'rm -rf /'",
		"mise exec -- rm -rf /",
		"mise x -- rm -rf /",
		"mise exec node@20 -- rm -rf /",
		"rtx exec -- rm -rf /",
		"direnv exec . rm -rf /",
		"direnv exec /tmp/proj rm -rf /",
		"asdf exec rm -rf /",
		"arch -x86_64 rm -rf /",
		"script -q /dev/null rm -rf /",
	}
	for _, c := range deny {
		if got := wrAction(c); got != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s), want deny", c, got, Classify(c))
		}
	}
	codeExec := []string{
		"script -qc 'ls' /dev/null",
		"watch 'ls; df'",
		"watch -n 1 'ls | head'",
		"nix-shell --run 'ls'",
		"nix shell nixpkgs#hello -c ls",
		"nix run nixpkgs#hello",
		"nix run nixpkgs#hello -- --version",
		"nix develop -c ls",
		"flock /tmp/l -c 'ls'",
		"mise exec -- ls",
		"mise x node@20 -- ls",
		"rtx exec -- ls",
		"direnv exec . ls",
	}
	for _, c := range codeExec {
		if !lgHas(c, CodeExecution) {
			t.Errorf("Analyze(%q).Effects = %v, want code_execution present", c, Analyze(c).Effects)
		}
		if wrAction(c) == Allow {
			t.Errorf("ActionForCommand(%q) = allow", c)
		}
		if lgHas(c, Unknown) {
			t.Errorf("Analyze(%q).Effects = %v: unknown means the wrapper was not understood", c, Analyze(c).Effects)
		}
	}
	// A plain command behind watch keeps its own class (no payload shell).
	if got := Classify("watch -n 2 df -h"); got != Safe {
		t.Errorf("Classify(watch -n 2 df -h) = %s, want safe", got)
	}
}

// Project-script runners are code execution by themselves.
func TestLeftover_ProjectRunnersPinned(t *testing.T) {
	for _, c := range []string{
		"pipenv run ls", "pipenv run python x.py", "poetry run ls", "poetry run pytest",
		"bundle exec ls", "bundle exec rspec", "uv run ls", "uv run python x.py", "cargo run", "cargo run --release",
	} {
		if !lgHas(c, CodeExecution) {
			t.Errorf("Analyze(%q).Effects = %v, want code_execution", c, Analyze(c).Effects)
		}
	}
}

// Wrapper option values must not be mistaken for the wrapper chain either:
// a following wrapper after a value-taking option is still unwrapped.
func TestLeftover_ChainedWrapperAfterOptionValue(t *testing.T) {
	cmds := []string{
		"timeout -s KILL 60 env FOO=1 rm -rf /",
		"nice -n 10 timeout -s KILL 60 sudo -u root rm -rf /",
		"timeout -s KILL 60 xargs rm -rf /",
		"stdbuf -oL timeout -k 5 60 sh -c 'rm -rf /'",
	}
	for _, c := range cmds {
		if wrAction(c) != Deny {
			t.Errorf("ActionForCommand(%q) = %s (class %s), want deny", c, wrAction(c), Classify(c))
		}
	}
	if got := Classify("timeout -s KILL 60 env -C /tmp ls"); got != Safe {
		t.Errorf("Classify(timeout -s KILL 60 env -C /tmp ls) = %s, want safe", got)
	}
}

// Brace sequences expand in bash, so words assembled from them name real
// paths: `/et{c..c}/shadow` is /etc/shadow.
func TestLeftover_BraceSequenceExpansion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"echo {1..3}", "echo 1 2 3"},
		{"echo {a..c}", "echo a b c"},
		{"echo {01..10}", "echo 01 02 03 04 05 06 07 08 09 10"},
		{"echo {1..10..2}", "echo 1 3 5 7 9"},
		{"echo {c..c}", "echo c"},
		{"echo {3..1}", "echo 3 2 1"},
		{"echo {c..a}", "echo c b a"},
		{"echo {a..e..2}", "echo a c e"},
		{"echo x{1..3}y", "echo x1y x2y x3y"},
		{"echo {-1..1}", "echo -1 0 1"},
		{"echo {08..10}", "echo 08 09 10"},
		{"echo {1..3}{a,b}", "echo 1a 1b 2a 2b 3a 3b"},
		{"echo {a,{1..2}}", "echo a 1 2"},
		// not sequences: left alone
		{"echo {1..}", "echo {1..}"},
		{"echo {a..bb}", "echo {a..bb}"},
		{"echo {1..b}", "echo {1..b}"},
		{"echo '{1..3}'", "echo '{1..3}'"},
		{"echo ${x}{1..2}", "echo ${x}1 ${x}2"},
		{"echo ${1..3}", "echo ${1..3}"},
	}
	for _, c := range cases {
		got := strings.Join(strings.Fields(expandBraces(c.in)), " ")
		if got != c.want {
			t.Errorf("expandBraces(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// a huge numeric range stays bounded and is not an overflow denial
	if out := expandBraces("for i in {1..100000}; do echo $i; done"); strings.Contains(out, braceOverflowToken) || len(out) > 2000 {
		t.Errorf("large numeric sequence not bounded: %d bytes, overflow=%v", len(out), strings.Contains(out, braceOverflowToken))
	}
}

func TestLeftover_BraceSequenceClassification(t *testing.T) {
	deny := []string{
		"cat /et{c..c}/shadow",
		"cat /e{t..t}c/shadow",
		"cat /etc/sha{d..d}ow",
		"rm -rf /{e..e}tc",
		"cat ~/.s{s..s}h/id_rsa",
	}
	for _, c := range deny {
		if wrAction(c) == Allow {
			t.Errorf("ActionForCommand(%q) = allow (class %s)", c, Classify(c))
		}
	}
	// local deletes through a sequence stay local, and plain use stays safe
	if got := Classify("rm -rf /tmp/x{1..3}"); got != LocalWrite {
		t.Errorf("Classify(rm -rf /tmp/x{1..3}) = %s, want local_write", got)
	}
	for _, c := range []string{"echo {1..3}", "ls /tmp/{a..c}"} {
		if wrAction(c) == Deny {
			t.Errorf("ActionForCommand(%q) = deny", c)
		}
	}
}

// A script reaches an interpreter without being a path operand: piped in,
// fed through a substitution, redirected to stdin, or named by an option of
// a tool that loads a program file. Each form executes the file's content,
// so an unread file must gate exactly like `bash x.sh`.
func TestLeftover_ReadLedgerGatesIndirectScriptForms(t *testing.T) {
	cases := []struct{ cmd, file string }{
		{"cat x.sh | bash", "x.sh"},
		{"cat x.sh | sh -s", "x.sh"},
		{"cat x.sh | sudo bash", "x.sh"},
		{"cat ./x.sh | bash -s -- arg", "x.sh"},
		{"cat x.py | python3", "x.py"},
		{"cat x.js | node", "x.js"},
		{"bash <(cat x.sh)", "x.sh"},
		{"source <(cat x.sh)", "x.sh"},
		{". <(cat x.sh)", "x.sh"},
		{`sh <<< "$(cat x.sh)"`, "x.sh"},
		{"bash < <(cat x.sh)", "x.sh"},
		{`eval "$(cat x.sh)"`, "x.sh"},
		{"eval `cat x.sh`", "x.sh"},
		{"python3 < x.py", "x.py"},
		{"node < x.js", "x.js"},
		{"awk -f x.awk", "x.awk"},
		{"gawk -f x.awk data", "x.awk"},
		{"awk --file=x.awk data", "x.awk"},
		{"awk --file x.awk data", "x.awk"},
		{"sed -f x.sed in", "x.sed"},
		{"sed --file=x.sed in", "x.sed"},
		{"sed -n -f x.sed in", "x.sed"},
		{"emacs --script x.el", "x.el"},
		{"emacs -Q --batch -l x.el", "x.el"},
		{"emacs --batch --load x.el", "x.el"},
		{"emacs --batch --load=x.el", "x.el"},
		{"vim -S x.vim", "x.vim"},
		{"vim -u x.vim -c q", "x.vim"},
		{"nvim -l x.lua", "x.lua"},
		{"nvim --headless -S x.vim", "x.vim"},
		{"find . -exec ./x.sh {} +", "x.sh"},
		{`find . -execdir bash x.sh {} \;`, "x.sh"},
		{"find . -name '*.c' -exec sh x.sh {} +", "x.sh"},
		{"xargs -I{} bash x.sh {}", "x.sh"},
		{"ls | xargs bash x.sh", "x.sh"},
		{"parallel bash x.sh ::: a", "x.sh"},
		{"make -f x.mk", "x.mk"},
		{"make --file=x.mk all", "x.mk"},
		{"make --makefile x.mk", "x.mk"},
		{"gdb -x x.gdb prog", "x.gdb"},
		{"gdb -batch -x x.gdb prog", "x.gdb"},
		{"gdb --command=x.gdb prog", "x.gdb"},
		{"gdb -batch -ex 'source x.py' prog", "x.py"},
		{"lldb -s x.lldb", "x.lldb"},
		{"lldb --source x.lldb", "x.lldb"},
		{"sqlite3 db '.read x.sql'", "x.sql"},
	}
	for _, c := range cases {
		ledgerSandbox(t)
		ledgerWrite(t, c.file, "echo hi\n", 0o644)
		if got := UnreadScriptTargets(c.cmd); !targetsContainBase(got, c.file) {
			t.Errorf("UnreadScriptTargets(%q) = %v, want %s gated while unread", c.cmd, got, c.file)
		}
		RecordRead(c.file)
		if got := UnreadScriptTargets(c.cmd); targetsContainBase(got, c.file) {
			t.Errorf("UnreadScriptTargets(%q) = %v after read, want %s licensed", c.cmd, got, c.file)
		}
	}
}

// Plain data uses of the same tools stay ungated.
func TestLeftover_ReadLedgerIndirectFormsDoNotOverGate(t *testing.T) {
	ledgerSandbox(t)
	ledgerWrite(t, "x.sh", "echo hi\n", 0o644)
	ledgerWrite(t, "data.txt", "hi\n", 0o644)
	for _, cmd := range []string{
		"cat x.sh | grep echo",
		"cat x.sh | wc -l",
		"grep -f x.sh data.txt",
		"diff <(cat x.sh) data.txt",
		"echo \"$(cat x.sh)\"",
		"cat x.sh | head -1",
		"make -n all",
		"find . -name x.sh",
		"find . -exec cat x.sh {} +",
		"xargs cat x.sh",
		"vim x.sh",
		"awk '{print $1}' x.sh",
		"sed -n 1p x.sh",
		"emacs x.sh",
	} {
		if got := UnreadScriptTargets(cmd); len(got) != 0 {
			t.Errorf("UnreadScriptTargets(%q) = %v, want no gate (the file is data)", cmd, got)
		}
	}
}

// Variables bound by the declaration builtins keep their literal value for
// later words, like a plain NAME=value assignment does.
func TestLeftover_DeclarationBuiltinsBindVariables(t *testing.T) {
	for _, assign := range []string{
		"export S=x.sh", "export -n S=x.sh", "declare S=x.sh", "declare -x S=x.sh", "declare -r S=x.sh",
		"declare -g S=x.sh", "typeset S=x.sh", "typeset -x S=x.sh", "readonly S=x.sh",
		"local S=x.sh", "export A=1 S=x.sh", "S=x.sh",
	} {
		ledgerSandbox(t)
		ledgerWrite(t, "x.sh", "echo hi\n", 0o644)
		cmd := assign + "; bash $S"
		if got := UnreadScriptTargets(cmd); !targetsContainBase(got, "x.sh") {
			t.Errorf("UnreadScriptTargets(%q) = %v, want x.sh gated", cmd, got)
		}
		RecordRead("x.sh")
		if got := UnreadScriptTargets(cmd); len(got) != 0 {
			t.Errorf("UnreadScriptTargets(%q) = %v after read, want licensed", cmd, got)
		}
	}
	// the known value also resolves write targets
	for _, c := range []string{"export T=/tmp/leftover-x; rm -f $T", "declare T=/tmp/leftover-x; rm -f $T", "readonly T=/tmp/leftover-x && rm -f $T"} {
		if got := Classify(c); got != LocalWrite {
			t.Errorf("Classify(%q) = %s, want local_write", c, got)
		}
	}
	if got := Classify("export D=/; rm -rf $D"); got != Destructive {
		t.Errorf("Classify(export D=/; rm -rf $D) = %s, want destructive", got)
	}
	// a bare export keeps the earlier value
	ledgerSandbox(t)
	ledgerWrite(t, "x.sh", "echo hi\n", 0o644)
	if got := UnreadScriptTargets("S=x.sh; export S; bash $S"); !targetsContainBase(got, "x.sh") {
		t.Errorf("bare export dropped the value: %v", got)
	}
}

// Values the shell computes or transforms are not recorded.
func TestLeftover_DeclarationBuiltinsDropDynamicValues(t *testing.T) {
	ledgerSandbox(t)
	ledgerWrite(t, "x.sh", "echo hi\n", 0o644)
	for _, cmd := range []string{
		"export S=$(cat names.txt); bash $S",
		"export S=`cat names.txt`; bash $S",
		"declare -u S=x.sh; bash $S",
		"declare -l S=X.SH; bash $S",
		"declare -i S=x.sh; bash $S",
		"declare -n S=x.sh; bash $S",
		"export S=x.sh || true; bash $S",
		"export S=x.sh & bash $S",
		"S=x.sh; export S=$(cat names.txt); bash $S",
		"S=x.sh; local S; bash $S",
	} {
		if got := UnreadScriptTargets(cmd); targetsContainBase(got, "x.sh") {
			t.Errorf("UnreadScriptTargets(%q) = %v: the value is not statically x.sh", cmd, got)
		}
		if wrAction(cmd) == Allow {
			t.Errorf("ActionForCommand(%q) = allow, want the unresolved $S to stay gated", cmd)
		}
	}
}
