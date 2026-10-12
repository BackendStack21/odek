package danger

import (
	"path/filepath"
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

// finalHuntHomeCwd makes the working directory the user's home.
func finalHuntHomeCwd(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(home)
	return home
}

// A bot launched from $HOME must still be able to search its sources and list
// files: only readers that print file content broadly are secret reads.
func TestRED_RecursiveReadersFromHomeAreNotOverStrict(t *testing.T) {
	home := finalHuntHomeCwd(t)
	for _, cmd := range []string{
		`rg TODO`, `rg --files`, `rg -n TODO .`, `grep -r TODO .`, `grep -rn TODO ` + home,
		`grep -rl foo .`, `rg -l foo ~`, `ag foo`, `find . -name '*.go' -exec grep -l foo {} +`,
		`find . -name '*.go' -exec grep -n TODO {} +`,
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
	dangerHunt12bAtLeast(t, SystemWrite,
		`rg .`, `rg ''`, `grep -r '' .`, `grep -r . ~`, `rg -v zzz`, `rg KEY`, `rg -i password .`,
		`rg -e foo -e .`, `grep -rE '^' .`,
		`tar czf /tmp/x.tgz .`, `cp -r . /tmp/copy`, `find . -exec cat {} +`,
		`find . -name '*.go' -exec grep -v zzz {} +`, `rg -uuu . ~`,
		`grep -r foo /proc/self`, `grep -r foo /etc`, `rg foo /`)
}

func TestRED_RecursiveReaderWildcardRootsAndPipes(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt12bAtLeast(t, SystemWrite,
		`rg KEY /ho?e/user`, `grep -r . /h*/user`, `rg -uu . /[h]ome/user`, `rg KEY /home/*`,
		`find ~ -type f | xargs cat`, `find /home/user -name '*.pem' | xargs cat`,
		`find ~ -type f -print0 | xargs -0 cat`, `find /home/user | xargs grep -h .`)
	for _, cmd := range []string{
		`find ~ -name '*.go' | xargs wc -l`, `find src | xargs cat`, `find . -name '*.go' | xargs cat`,
		`rg TODO /ho?e/user`, `ls /ho?e/user`,
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
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
