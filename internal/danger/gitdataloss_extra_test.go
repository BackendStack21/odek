package danger

import "testing"

func TestGitDataLossRefAndObjectVerbs(t *testing.T) {
	chdirUnarmedRepo(t)
	for _, cmd := range []string{
		"git branch -f main HEAD~3",
		"git branch -M old new",
		"git branch -D feature",
		"git tag -d v1",
		"git tag --delete v1",
		"git tag -f v1 HEAD",
		"git rm --force a.go",
		"git prune --expire now",
		"git prune --expire=now",
		"git reflog delete HEAD@{1}",
	} {
		if got := Classify(cmd); Rank(got) < Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want at least system_write", cmd, got)
		}
	}
	for _, cmd := range []string{
		"git rm --cached a.go",
		"git rm -r --cached dir",
		"git prune -n",
		"git tag v1",
		"git tag -l",
		"git branch feature",
		"git branch --list",
		"git reflog show",
	} {
		if got := Classify(cmd); Rank(got) >= Rank(SystemWrite) {
			t.Errorf("Classify(%q) = %s, want below system_write", cmd, got)
		}
	}
}
