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
