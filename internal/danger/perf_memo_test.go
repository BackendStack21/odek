package danger

import (
	"os"
	"testing"
)

func TestRED_Danger_ResourceTokenStatMemoizedPerAnalysis(t *testing.T) {
	calls := 0
	orig := statPath
	statPath = func(p string) (os.FileInfo, error) {
		calls++
		return orig(p)
	}
	defer func() { statPath = orig }()

	end := beginPathMemo()
	for i := 0; i < 5; i++ {
		classifyResourceToken("some/relative/operand.txt")
	}
	end()
	if calls != 1 {
		t.Fatalf("os.Stat ran %d times for one operand in one analysis, want 1", calls)
	}

	// Outside an analysis nothing is remembered.
	calls = 0
	classifyResourceToken("some/relative/operand.txt")
	classifyResourceToken("some/relative/operand.txt")
	if calls != 2 {
		t.Fatalf("stat outside an analysis ran %d times, want 2 (no memo)", calls)
	}
}

func TestRED_Danger_NoRegexpCompiledPerCall(t *testing.T) {
	if n := testing.AllocsPerRun(50, func() {
		awkScriptHasShellExec("BEGIN { print 1 }")
	}); n > 2 {
		t.Fatalf("awkScriptHasShellExec allocates %v per call; a regexp is being compiled per call", n)
	}
	toks := []string{"jq", ".scripts.x = 1", "package.json"}
	if n := testing.AllocsPerRun(50, func() {
		isPersistenceWrite("jq", toks)
	}); n > 40 {
		t.Fatalf("isPersistenceWrite allocates %v per call; a regexp is being compiled per call", n)
	}
}

func TestPerfMemo_AwkAndJqVerdictsUnchanged(t *testing.T) {
	for tok, want := range map[string]bool{
		"BEGIN { system(\"id\") }": true,
		"'{ print | \"sh\" }'":     true,
		"{ print $1 }":             false,
		"{ print @x }":             true,
	} {
		if got := awkScriptHasShellExec(tok); got != want {
			t.Errorf("awkScriptHasShellExec(%q) = %v, want %v", tok, got, want)
		}
	}
	if !isPersistenceWrite("jq", []string{"jq", ".scripts.preinstall=\"x\"", "package.json"}) {
		t.Error("jq assignment into package.json scripts must be persistence")
	}
	if isPersistenceWrite("jq", []string{"jq", ".scripts", "package.json"}) {
		t.Error("jq read of package.json scripts must not be persistence")
	}
}
