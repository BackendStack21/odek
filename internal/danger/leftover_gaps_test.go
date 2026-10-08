package danger

import "testing"

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
