package danger

import "testing"

// Verbs that only escalate when the repository they target is armed stay at
// their ordinary class in a clean repository and outside any repository.
func TestGitArmingVerbsStayOrdinaryWhenUnarmed(t *testing.T) {
	chdirUnarmedRepo(t)
	for cmd, want := range map[string]RiskClass{
		"git revert --no-edit HEAD": SystemWrite,
		"git ls-files -m":           Safe,
		"git grep x":                Safe,
		"git blame a":               Safe,
		"git diff-files":            Safe,
		"git describe --dirty":      Safe,
		"git push origin main":      NetworkEgress,
		"git pull":                  NetworkEgress,
		"git fetch":                 NetworkEgress,
	} {
		got := Classify(cmd)
		if want == SystemWrite {
			// revert rewrites history: any class is fine as long as it is
			// not escalated to code execution by an unarmed repository.
			if Rank(got) >= Rank(CodeExecution) {
				t.Errorf("unarmed repo: Classify(%q) = %s, want below code_execution", cmd, got)
			}
			continue
		}
		if got != want {
			t.Errorf("unarmed repo: Classify(%q) = %s, want %s", cmd, got, want)
		}
	}
}

func TestGitArmingVerbsOutsideRepository(t *testing.T) {
	isolateGitEnv(t)
	t.Chdir(t.TempDir())
	for _, cmd := range []string{"git ls-files", "git grep x", "git push origin main", "git fetch"} {
		if got := Classify(cmd); Rank(got) >= Rank(CodeExecution) {
			t.Errorf("no repository: Classify(%q) = %s, want below code_execution", cmd, got)
		}
	}
}

func TestGitArmingRevertAndPullOpenEditor(t *testing.T) {
	redArmedRepo(t, "[core]\n\teditor = ./evil\n")
	if got := Classify("git revert HEAD"); Rank(got) < Rank(CodeExecution) {
		t.Errorf("revert with an armed editor = %s, want >= code_execution", got)
	}
	if got := Classify("git revert --no-edit HEAD"); Rank(got) >= Rank(CodeExecution) {
		t.Errorf("revert --no-edit never opens the editor, got %s", got)
	}
}
