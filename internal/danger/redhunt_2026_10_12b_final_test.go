package danger

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// finalHuntComposeInput builds an xargs -I command whose placeholder is
// repeated reps times and whose static input has lines lines of words words.
func finalHuntComposeInput(lines, words, reps int) string {
	var in []string
	for i := 0; i < lines; i++ {
		in = append(in, strings.TrimSpace(strings.Repeat("wx ", words)))
	}
	return "printf '" + strings.Join(in, "\\n") + "' | xargs -I{} echo " + strings.Repeat("{}x ", reps)
}

func finalHuntMeasure(f func()) (time.Duration, uint64) {
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	t0 := time.Now()
	f()
	d := time.Since(t0)
	runtime.ReadMemStats(&m1)
	return d, m1.TotalAlloc - m0.TotalAlloc
}

func TestRED_ArgvCompositionTotalSizeBounded(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cmd := finalHuntComposeInput(256, 20, 60)
	if len(cmd) > MaxCommandBytes {
		t.Fatalf("test input too large: %d", len(cmd))
	}
	var got RiskClass
	_, alloc := finalHuntMeasure(func() { got = Classify(cmd) })
	if alloc > 256<<20 {
		t.Errorf("classifying a %d byte composer command allocated %d MiB", len(cmd), alloc>>20)
	}
	if got != Unknown {
		t.Errorf("oversized composition = %s, want unknown (fail closed)", got)
	}
	cfg := &DangerousConfig{Denylist: []string{"git push"}}
	var act Action
	_, alloc = finalHuntMeasure(func() { act = cfg.ActionForCommand(cmd) })
	if alloc > 256<<20 {
		t.Errorf("denylist scan allocated %d MiB", alloc>>20)
	}
	if act != Deny {
		t.Errorf("oversized composition with denylist = %s, want deny", act)
	}
	if got := Classify(`echo / | xargs rm -rf`); got != Destructive {
		t.Errorf("small composition = %s, want destructive", got)
	}
}

func TestRED_ArgvCompositionScalesLinearly(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	if raceEnabled || testing.Short() {
		t.Skip("timing ratios need an uninstrumented full run")
	}
	small := finalHuntComposeInput(64, 10, 10)
	big := finalHuntComposeInput(256, 40, 40)
	dSmall, _ := finalHuntMeasure(func() { Classify(small) })
	dBig, _ := finalHuntMeasure(func() { Classify(big) })
	if dBig > 50*dSmall+200*time.Millisecond {
		t.Errorf("classify time grew %v -> %v", dSmall, dBig)
	}
}

func TestRED_GlobEnvironSingleLetterPrefix(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat /proc/self/e*`, `cat /proc/self/[e]*`, `cat /proc/1/e?viron`, `cat /proc/*/e*`)
	for _, cmd := range []string{`cat /proc/self/s*`, `cat /proc/self/status`, `cat /proc/self/c*`} {
		if got := Classify(cmd); got != Safe {
			t.Errorf("Classify(%q) = %s, want safe", cmd, got)
		}
	}
}

func TestRED_GitConfigHomeOverride(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`HOME=/tmp/evil git status`, `XDG_CONFIG_HOME=/tmp/evil git status`,
		`export HOME=/tmp/evil; git log`, `export XDG_CONFIG_HOME=/tmp/evil; git log`,
		`env HOME=/tmp/evil git diff`)
	for _, cmd := range []string{`HOME=/home/user git status`, `HOME=$HOME git status`,
		`XDG_CONFIG_HOME=$HOME/.config git status`, `XDG_CONFIG_HOME=/home/user/.config git status`} {
		dangerHunt12bAtMost(t, Classify(`git status`), cmd)
	}
}

func dangerHunt12bAtMost(t *testing.T, max RiskClass, cmd string) {
	t.Helper()
	if got := Classify(cmd); Rank(got) > Rank(max) {
		t.Errorf("Classify(%q) = %s, want at most %s", cmd, got, max)
	}
}

func TestRED_GlobShellRCAndHistory(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`cat ~/.bash*`, `cat ~/.*history`, `cat ~/.zsh*`, `cat ~/.*rc`, `cat ~/.bash_hist*`,
		`cat ~/.zsh_h?story`, `cat ~/.bashr?`, `cat /home/user/.bash*`)
	for _, cmd := range []string{`cat ~/*.txt`, `ls ~/Doc*`, `cat ~/notes/.bash*`} {
		if got := Classify(cmd); Rank(got) > Rank(LocalWrite) {
			t.Errorf("Classify(%q) = %s, want ordinary", cmd, got)
		}
	}
}
