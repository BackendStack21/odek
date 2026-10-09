package danger

import "testing"

// isGitDataLoss documents the git verbs that irreversibly destroy uncommitted
// work or history and must never run silently (system_write, prompt). These
// equivalents delete the same data but classify safe.
func TestRED_GitDataLossVerbsNotSafe(t *testing.T) {
	for _, cmd := range []string{
		"git rm -rf .",
		"git rm -f tracked.go",
		"git prune --expire=now",
		"git reflog delete HEAD@{0}",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write (git clean -f / git reset --hard are system_write)", cmd, got)
		}
	}
}
