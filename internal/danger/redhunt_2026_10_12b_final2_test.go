package danger

import (
	"strings"
	"testing"
)

func TestRED_NarrowSearchPatternIsLiteral(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`rg '[[:print:]]' ~`, `rg '[[:alnum:]]' ~`, `grep -rE '[[:alpha:]]*' ~`, `rg '(abc)?' ~`,
		`grep -rE 'a*b*c*' ~`, `rg '\w\w\w' ~`, `rg '[a-z0-9]' ~`, `rg '[^x][^x][^x]' ~`,
		`rg 'z*z*z*' ~`, `rg '' ~`, `rg -F '' ~`, `grep -r "" ~`, `rg 'foo.*' ~`)
	for _, cmd := range []string{`rg TODO ~`, `rg -F 'a.b.c' ~`, `grep -rn FIXME_later ~`, `rg foo_bar ~`} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
}

func TestRED_ArgvCompositionCommandCapFailsClosed(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	benign := strings.Repeat("a\\n", 300)
	if got := Classify(`printf '` + benign + `/' | xargs -I{} rm -rf {}`); Rank(got) < Rank(Destructive) && got != Unknown {
		t.Errorf("300 lines then / = %s, want unknown or destructive", got)
	}
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	cmd := `printf '` + strings.Repeat("status\\n", 300) + `push' | xargs -I{} git {}`
	if act := cfg.ActionForCommand(cmd); act != Deny {
		t.Errorf("denylist with 300 lines then push = %s, want deny", act)
	}
}

func TestRED_ProcTaskEnvironGlobs(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat /proc/self/task/1/e*`, `cat /proc/self/task/*/e*`, `cat /proc/*/task/*/e*`,
		`cat /proc/self/task/1/environ`)
	for _, cmd := range []string{`cat /proc/self/task/1/status`, `cat /proc/self/task/*/s*`} {
		dangerHunt12bAtMost(t, Safe, cmd)
	}
}

func TestRED_OtherAccountsShellHistory(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat /home/*/.bash_history`, `cat /home/other/.bash_history`, `cat /root/.bash_history`,
		`cat /Users/*/.zsh_history`, `cat /home/other/.bashrc`, `cat /home/*/.zsh*`)
}
