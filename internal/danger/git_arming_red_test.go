package danger

import (
	"path/filepath"
	"testing"
)

// Verified against real git 2.x: with core.fsmonitor set, each of these verbs
// spawns the fsmonitor program; with an executable pre-push hook, `git push`
// spawns the hook. The classifier only escalates the verbs in gitVerbArmMask.

func redArmedRepo(t *testing.T, config string, hooks ...string) {
	t.Helper()
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), config)
	for _, h := range hooks {
		writeTestFile(t, filepath.Join(repo, ".git", "hooks", h), "#!/bin/sh\nid\n", 0o755)
	}
	t.Chdir(repo)
}

func TestRED_GitFsmonitorVerbsNotEscalated(t *testing.T) {
	redArmedRepo(t, "[core]\n\tfsmonitor = ./evil\n")
	for _, c := range []string{
		"git revert --no-edit HEAD",
		"git rm -f b",
		"git mv a a2",
		"git update-index --refresh",
		"git diff-files",
		"git diff-index HEAD",
		"git ls-files -m",
		"git grep x",
		"git blame a",
		"git pull",
		"git fetch",
		"git reset --hard HEAD",
		"git clean -fdn",
	} {
		if got := Classify(c); Rank(got) < Rank(CodeExecution) {
			t.Errorf("fsmonitor-armed repo: Classify(%q) = %s, want >= code_execution", c, got)
		}
	}
}

func TestRED_GitPrePushHookNotEscalated(t *testing.T) {
	redArmedRepo(t, "", "pre-push")
	if got := Classify("git push origin main"); Rank(got) < Rank(CodeExecution) {
		t.Errorf("repo with executable pre-push hook: git push = %s, want >= code_execution", got)
	}
}
