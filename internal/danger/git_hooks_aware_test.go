package danger

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the repository-aware escalation of ordinary git verbs: a
// verb that can run hooks, an fsmonitor, filters, diff/textconv drivers, merge
// drivers or an editor is code_execution only when the repository (or the
// user's git configuration) actually arms one of them, and fails closed when
// the repository cannot be determined.

// isolateGitEnv gives the test an empty HOME, no XDG config, a hermetic
// system config and none of the process-level GIT_* overrides, so the verdict
// depends only on the repositories the test builds.
func isolateGitEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{
		"XDG_CONFIG_HOME", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_EXEC_PATH",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_NOSYSTEM", "GIT_EXTERNAL_DIFF", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	saved := gitSystemConfigPath
	gitSystemConfigPath = filepath.Join(home, "no-system-gitconfig")
	t.Cleanup(func() { gitSystemConfigPath = saved })
	return home
}

// makeRepo lays out a repository at dir by hand and returns dir.
func makeRepo(t *testing.T, dir, config string) string {
	t.Helper()
	g := filepath.Join(dir, ".git")
	for _, d := range []string{"hooks", "objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n", 0o644)
	writeTestFile(t, filepath.Join(g, "config"), "[core]\n\trepositoryformatversion = 0\n"+config, 0o644)
	return dir
}

// chdirUnarmedRepo moves the test into a fresh repository with nothing armed
// and an isolated git environment, so a pin on an ordinary git verb does not
// depend on the repository or home directory the test run happens to use.
func chdirUnarmedRepo(t *testing.T) string {
	t.Helper()
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	return repo
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func hasEffect(cmd string, cls RiskClass) bool {
	for _, e := range Analyze(cmd).Effects {
		if e == cls {
			return true
		}
	}
	return false
}

// expectCodeExec asserts whether cmd carries a code_execution effect.
func expectCodeExec(t *testing.T, cmd string, want bool) {
	t.Helper()
	if got := hasEffect(cmd, CodeExecution); got != want {
		t.Errorf("%q: code_execution effect = %v, want %v (effects %v)", cmd, got, want, Analyze(cmd).Effects)
	}
}

func TestGitHooksAware_UnarmedRepositoryIsRoutine(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit.sample"), "#!/bin/sh\n", 0o755)
	t.Chdir(repo)
	cases := []struct {
		cmd string
		cls RiskClass
	}{
		{"git status", Safe},
		{"git add .", Safe},
		{"git commit -m x", Safe},
		{"git commit -am 'fix: thing'", Safe},
		{"git commit --amend --no-edit", Safe},
		{"git diff", Safe},
		{"git diff --stat HEAD~1", Safe},
		{"git log --oneline", Safe},
		{"git log -p", Safe},
		{"git show HEAD", Safe},
		{"git merge feature --no-edit", Safe},
		{"git merge --ff-only feature", Safe},
		{"git checkout main", Safe},
		{"git checkout -b feature", Safe},
		{"git switch main", Safe},
		{"git stash", Safe},
		{"git stash pop", Safe},
		{"git gc", Safe},
		{"git rebase main", SystemWrite},
		{"git cherry-pick abc123", SystemWrite},
		{"git am patch.mbox", SystemWrite},
		{"git restore --staged file", Safe},
		{"git submodule status", Safe},
		{"git worktree list", Safe},
		// Other effects are unchanged.
		{"git checkout -- .", SystemWrite},
		{"git checkout -f main", SystemWrite},
		{"git restore .", SystemWrite},
		{"git clean -fdx", SystemWrite},
		{"git reset --hard", SystemWrite},
		{"git stash drop", SystemWrite},
		{"git push origin main", NetworkEgress},
		{"git pull", NetworkEgress},
		{"git fetch origin", NetworkEgress},
		{"git submodule update --init", NetworkEgress},
	}
	for _, tc := range cases {
		if got := Classify(tc.cmd); got != tc.cls {
			t.Errorf("Classify(%q) = %s, want %s", tc.cmd, got, tc.cls)
		}
	}
}

func TestGitHooksAware_ExecutableHook(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")

	writeTestFile(t, hook, "#!/bin/sh\nexit 0\n", 0o755)
	for _, cmd := range []string{
		"git commit -m x", "git merge feature --no-edit", "git checkout main", "git switch main",
		"git rebase main", "git cherry-pick abc", "git am p.mbox", "git stash", "git gc",
		"git worktree add ../w",
	} {
		expectCodeExec(t, cmd, true)
	}
	// --no-verify does not skip post-commit and friends, so it does not disarm.
	expectCodeExec(t, "git commit --no-verify -m x", true)
	// Verbs that never run hooks stay routine in a hook-armed repository.
	for _, cmd := range []string{"git status", "git add .", "git diff", "git log -p", "git show HEAD", "git restore --staged f"} {
		expectCodeExec(t, cmd, false)
	}

	// Not executable: git ignores it.
	writeTestFile(t, hook, "#!/bin/sh\n", 0o644)
	expectCodeExec(t, "git commit -m x", false)

	// Executable but not a hook name.
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "my-helper"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git commit -m x", false)

	// Another real hook name.
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "post-checkout"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git checkout main", true)
	expectCodeExec(t, "git commit -m x", true)

	// Deterministic: removing the hook returns the verdict to unarmed.
	if err := os.Remove(filepath.Join(repo, ".git", "hooks", "post-checkout")); err != nil {
		t.Fatal(err)
	}
	expectCodeExec(t, "git checkout main", false)
}

func TestGitHooksAware_SampleHooksAreInert(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	for _, name := range []string{"pre-commit.sample", "commit-msg.sample", "post-update.sample", "pre-push.sample"} {
		writeTestFile(t, filepath.Join(repo, ".git", "hooks", name), "#!/bin/sh\n", 0o755)
	}
	t.Chdir(repo)
	for _, cmd := range []string{"git commit -m x", "git checkout main", "git merge x --no-edit", "git status"} {
		expectCodeExec(t, cmd, false)
	}
}

func TestGitHooksAware_ConfigArming(t *testing.T) {
	cases := []struct {
		name   string
		config string
		// armed lists verbs that must carry code_execution; routine lists
		// verbs that must not.
		armed   []string
		routine []string
	}{
		{"hooksPath", "[core]\n\thooksPath = .githooks\n",
			[]string{"git commit -m x", "git checkout main", "git merge x --no-edit"},
			[]string{"git status", "git add .", "git diff"}},
		{"hooksPath neutral", "[core]\n\thooksPath = /dev/null\n",
			nil, []string{"git commit -m x", "git checkout main"}},
		{"hooksPath empty", "[core]\n\thooksPath =\n",
			nil, []string{"git commit -m x"}},
		{"fsmonitor program", "[core]\n\tfsmonitor = .git/hooks/fsmonitor-watchman\n",
			[]string{"git status", "git add .", "git diff", "git commit -m x", "git checkout main"},
			[]string{"git log", "git show HEAD"}},
		{"fsmonitor builtin", "[core]\n\tfsmonitor = true\n",
			nil, []string{"git status", "git add ."}},
		{"fsmonitor off", "[core]\n\tfsmonitor = false\n",
			nil, []string{"git status"}},
		{"filter clean", "[filter \"x\"]\n\tclean = sed s/a/b/\n",
			[]string{"git add .", "git status", "git commit -m x", "git checkout main", "git diff"},
			[]string{"git log", "git show HEAD", "git gc"}},
		{"filter smudge", "[filter \"x\"]\n\tsmudge = cat\n",
			[]string{"git checkout main"}, nil},
		{"filter process", "[filter.x]\n\tprocess = ./filter-driver\n",
			[]string{"git add ."}, nil},
		{"filter lfs", "[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n\trequired = true\n",
			nil, []string{"git add .", "git checkout main", "git status"}},
		{"filter lfs lookalike", "[filter \"lfs\"]\n\tclean = git-lfs clean %f | sh\n",
			[]string{"git add ."}, nil},
		{"diff external", "[diff]\n\texternal = difft\n",
			[]string{"git diff"}, []string{"git status", "git add .", "git log -p"}},
		{"diff driver command", "[diff \"d\"]\n\tcommand = mydiff\n",
			[]string{"git diff"}, []string{"git status"}},
		{"diff textconv", "[diff \"pdf\"]\n\ttextconv = pdftotext\n",
			[]string{"git diff", "git log -p", "git show HEAD", "git stash show -p"},
			[]string{"git status", "git add .", "git commit -m x", "git diff --no-textconv", "git log --no-textconv -p"}},
		{"merge driver", "[merge \"m\"]\n\tdriver = my-merge %O %A %B\n",
			[]string{"git merge x --no-edit", "git checkout main", "git rebase main", "git cherry-pick abc"},
			[]string{"git status", "git add .", "git commit -m x", "git diff"}},
		{"interactive diffFilter", "[interactive]\n\tdiffFilter = delta --color-only\n",
			[]string{"git add -p", "git add ."}, []string{"git status", "git commit -m x"}},
		{"submodule update exec", "[submodule \"s\"]\n\tupdate = !make\n",
			[]string{"git submodule sync"}, []string{"git status", "git commit -m x"}},
		{"submodule update builtin", "[submodule \"s\"]\n\tupdate = checkout\n",
			nil, []string{"git submodule status"}},
		{"hook command", "[hook \"h\"]\n\tcommand = ./run-hook\n\tevent = pre-commit\n",
			[]string{"git commit -m x"}, []string{"git status"}},
		{"include", "[include]\n\tpath = ../extra.cfg\n",
			[]string{"git status", "git add .", "git commit -m x", "git log"}, nil},
		{"includeIf", "[includeIf \"gitdir:~/work/\"]\n\tpath = ~/.gitconfig-work\n",
			[]string{"git status", "git commit -m x"}, nil},
		{"unrelated keys", "[user]\n\tname = A\n\temail = a@b.c\n[alias]\n\tco = checkout\n[core]\n\teditor =\n[remote \"origin\"]\n\turl = https://example.com/x.git\n",
			nil, []string{"git status", "git commit -m x", "git checkout main", "git add ."}},
		{"mixed case and inline comment", "[CORE]\n\tFsMonitor = watchman # use it\n",
			[]string{"git status"}, nil},
		{"line continuation", "[core]\n\thooksPath = \\\n.githooks\n",
			[]string{"git commit -m x"}, nil},
		{"same-line header", "[core] fsmonitor = hook\n",
			[]string{"git status"}, nil},
		{"commented out", "# [core]\n#\tfsmonitor = hook\n; hooksPath = x\n",
			nil, []string{"git status", "git commit -m x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitEnv(t)
			repo := makeRepo(t, t.TempDir(), tc.config)
			t.Chdir(repo)
			for _, cmd := range tc.armed {
				expectCodeExec(t, cmd, true)
			}
			for _, cmd := range tc.routine {
				expectCodeExec(t, cmd, false)
			}
		})
	}
}

func TestGitHooksAware_EditorVerbs(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "[core]\n\teditor = vim\n[sequence]\n\teditor = my-todo-editor\n")
	t.Chdir(repo)
	for _, cmd := range []string{
		"git commit", "git commit --amend", "git commit -a", "git commit -e -m x", "git commit --edit -m x",
		"git commit -c HEAD", "git merge feature", "git merge --edit feature", "git rebase -i HEAD~3",
		"git rebase --interactive main", "git rebase --continue", "git cherry-pick -e abc", "git cherry-pick --edit abc",
	} {
		expectCodeExec(t, cmd, true)
	}
	for _, cmd := range []string{
		"git commit -m x", "git commit -am x", "git commit --message=x", "git commit -F msg.txt",
		"git commit -C HEAD", "git commit --amend --no-edit", "git commit --fixup=abc",
		"git merge --no-edit feature", "git merge --ff-only feature", "git merge --squash feature",
		"git rebase main", "git cherry-pick abc", "git status", "git add .", "git checkout main", "git stash",
	} {
		expectCodeExec(t, cmd, false)
	}
	// Without any editor configured nothing is armed, even for editor verbs.
	repo2 := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo2)
	for _, cmd := range []string{"git commit", "git merge feature", "git rebase -i HEAD~3"} {
		expectCodeExec(t, cmd, false)
	}
}

func TestGitHooksAware_GlobalConfigArms(t *testing.T) {
	home := isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	expectCodeExec(t, "git commit -m x", false)

	global := filepath.Join(home, ".gitconfig")
	writeTestFile(t, global, "[user]\n\tname = A\n[core]\n\thooksPath = ~/.githooks\n", 0o644)
	expectCodeExec(t, "git commit -m x", true)
	expectCodeExec(t, "git status", false)

	writeTestFile(t, global, "[core]\n\tfsmonitor = fsm\n", 0o644)
	expectCodeExec(t, "git status", true)

	writeTestFile(t, global, "[includeIf \"gitdir:~/work/\"]\n\tpath = ~/.gitconfig-work\n", 0o644)
	expectCodeExec(t, "git status", true)

	writeTestFile(t, global, "[user]\n\tname = A\n", 0o644)
	expectCodeExec(t, "git status", false)

	// XDG location.
	writeTestFile(t, filepath.Join(home, ".config", "git", "config"), "[core]\n\thooksPath = x\n", 0o644)
	expectCodeExec(t, "git commit -m x", true)
	os.Remove(filepath.Join(home, ".config", "git", "config"))
	expectCodeExec(t, "git commit -m x", false)

	// Alternate global file named by the environment replaces ~/.gitconfig.
	alt := filepath.Join(t.TempDir(), "alt.cfg")
	writeTestFile(t, alt, "[core]\n\tfsmonitor = fsm\n", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", alt)
	expectCodeExec(t, "git status", true)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	os.Unsetenv("GIT_CONFIG_GLOBAL")

	// System config.
	sys := filepath.Join(t.TempDir(), "gitconfig")
	writeTestFile(t, sys, "[core]\n\thooksPath = /etc/hooks\n", 0o644)
	gitSystemConfigPath = sys
	expectCodeExec(t, "git commit -m x", true)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	expectCodeExec(t, "git commit -m x", false)
}

func TestGitHooksAware_ProcessEnvironment(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	expectCodeExec(t, "git status", false)

	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_EXEC_PATH", "GIT_CONFIG_PARAMETERS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "/elsewhere")
			expectCodeExec(t, "git status", true)
		})
	}
	t.Run("config count hooksPath", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
		t.Setenv("GIT_CONFIG_VALUE_0", "/evil")
		expectCodeExec(t, "git commit -m x", true)
	})
	t.Run("config count benign", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "credential.interactive")
		t.Setenv("GIT_CONFIG_VALUE_0", "false")
		expectCodeExec(t, "git commit -m x", false)
	})
	t.Run("config count malformed", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "two")
		expectCodeExec(t, "git status", true)
	})
	t.Run("external diff", func(t *testing.T) {
		t.Setenv("GIT_EXTERNAL_DIFF", "/tmp/x")
		expectCodeExec(t, "git diff", true)
		expectCodeExec(t, "git status", false)
	})
	t.Run("editor", func(t *testing.T) {
		t.Setenv("GIT_EDITOR", "vim")
		expectCodeExec(t, "git commit", true)
		expectCodeExec(t, "git commit -m x", false)
		t.Setenv("GIT_EDITOR", "true")
		expectCodeExec(t, "git commit", false)
	})
}

func TestGitHooksAware_WorktreeAndSubmoduleGitFiles(t *testing.T) {
	isolateGitEnv(t)
	root := t.TempDir()
	main := makeRepo(t, filepath.Join(root, "main"), "")
	// Linked worktree: .git file -> <main>/.git/worktrees/wt with commondir.
	wtGit := filepath.Join(main, ".git", "worktrees", "wt")
	writeTestFile(t, filepath.Join(wtGit, "commondir"), "../..\n", 0o644)
	writeTestFile(t, filepath.Join(wtGit, "HEAD"), "ref: refs/heads/wt\n", 0o644)
	wt := filepath.Join(root, "wt")
	writeTestFile(t, filepath.Join(wt, ".git"), "gitdir: "+wtGit+"\n", 0o644)
	sub := filepath.Join(wt, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(sub)
	expectCodeExec(t, "git commit -m x", false)
	expectCodeExec(t, "git status", false)

	// The worktree shares the main repository's hooks and config.
	writeTestFile(t, filepath.Join(main, ".git", "hooks", "pre-commit"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git commit -m x", true)
	os.Remove(filepath.Join(main, ".git", "hooks", "pre-commit"))
	expectCodeExec(t, "git commit -m x", false)
	writeTestFile(t, filepath.Join(main, ".git", "config"), "[core]\n\tfsmonitor = fsm\n", 0o644)
	expectCodeExec(t, "git status", true)
	writeTestFile(t, filepath.Join(main, ".git", "config"), "", 0o644)
	// Per-worktree config.
	writeTestFile(t, filepath.Join(wtGit, "config.worktree"), "[core]\n\thooksPath = h\n", 0o644)
	expectCodeExec(t, "git commit -m x", true)
	os.Remove(filepath.Join(wtGit, "config.worktree"))

	// Relative gitdir in the .git file.
	rel := filepath.Join(root, "rel")
	writeTestFile(t, filepath.Join(rel, ".git"), "gitdir: ../main/.git/worktrees/wt\n", 0o644)
	t.Chdir(rel)
	expectCodeExec(t, "git commit -m x", false)

	// Submodule: .git file -> <super>/.git/modules/sm, own hooks and config.
	super := makeRepo(t, filepath.Join(root, "super"), "")
	smGit := filepath.Join(super, ".git", "modules", "sm")
	for _, d := range []string{"hooks", "objects", "refs"} {
		os.MkdirAll(filepath.Join(smGit, d), 0o755)
	}
	writeTestFile(t, filepath.Join(smGit, "HEAD"), "ref: refs/heads/main\n", 0o644)
	writeTestFile(t, filepath.Join(smGit, "config"), "[core]\n\tworktree = ../../../sm\n", 0o644)
	sm := filepath.Join(super, "sm")
	writeTestFile(t, filepath.Join(sm, ".git"), "gitdir: ../.git/modules/sm\n", 0o644)
	t.Chdir(sm)
	expectCodeExec(t, "git commit -m x", false)
	writeTestFile(t, filepath.Join(smGit, "hooks", "pre-commit"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git commit -m x", true)
	os.Remove(filepath.Join(smGit, "hooks", "pre-commit"))
	expectCodeExec(t, "git commit -m x", false)

	// The superproject's verbs see the submodule's repository state too.
	t.Chdir(super)
	expectCodeExec(t, "git status", false)
	writeTestFile(t, filepath.Join(smGit, "config"), "[core]\n\tfsmonitor = fsm\n", 0o644)
	expectCodeExec(t, "git status", true)
	writeTestFile(t, filepath.Join(smGit, "config"), "", 0o644)
	writeTestFile(t, filepath.Join(smGit, "hooks", "post-checkout"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git checkout main", true)
	expectCodeExec(t, "git submodule update --init", true)
	expectCodeExec(t, "git status", false)

	// Nested submodule names (a/b) live in nested directories.
	os.Remove(filepath.Join(smGit, "hooks", "post-checkout"))
	nested := filepath.Join(super, ".git", "modules", "libs", "inner")
	for _, d := range []string{"hooks", "objects", "refs"} {
		os.MkdirAll(filepath.Join(nested, d), 0o755)
	}
	writeTestFile(t, filepath.Join(nested, "HEAD"), "ref: refs/heads/main\n", 0o644)
	writeTestFile(t, filepath.Join(nested, "hooks", "post-merge"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git merge x --no-edit", true)
}

func TestGitHooksAware_RealGitInit(t *testing.T) {
	if _, err := lookGit(); err != nil {
		t.Skip("git not installed")
	}
	isolateGitEnv(t)
	dir := t.TempDir()
	if out, err := runGit(dir, "init", "-q", "."); err != nil {
		t.Skipf("git init failed: %v %s", err, out)
	}
	t.Chdir(dir)
	// `git init` copies only .sample hooks.
	for _, cmd := range []string{"git status", "git commit -m x", "git checkout main", "git add ."} {
		expectCodeExec(t, cmd, false)
	}
	writeTestFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "#!/bin/sh\n", 0o755)
	expectCodeExec(t, "git commit -m x", true)

	// A real linked worktree.
	if out, err := runGit(dir, "-c", "user.name=n", "-c", "user.email=e@x", "commit", "-q", "--allow-empty", "-m", "i"); err != nil {
		t.Skipf("git commit failed: %v %s", err, out)
	}
	wt := filepath.Join(t.TempDir(), "linked")
	if out, err := runGit(dir, "worktree", "add", "-q", "-b", "other", wt); err != nil {
		t.Skipf("git worktree add failed: %v %s", err, out)
	}
	t.Chdir(wt)
	expectCodeExec(t, "git commit -m x", true)
	os.Remove(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	expectCodeExec(t, "git commit -m x", false)
}

func TestGitHooksAware_RepositorySelection(t *testing.T) {
	isolateGitEnv(t)
	root := t.TempDir()
	plain := makeRepo(t, filepath.Join(root, "plain"), "")
	armed := makeRepo(t, filepath.Join(root, "armed"), "[core]\n\tfsmonitor = fsm\n")
	writeTestFile(t, filepath.Join(armed, ".git", "hooks", "pre-commit"), "#!/bin/sh\n", 0o755)

	t.Run("-C armed from plain cwd", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "git status", false)
		expectCodeExec(t, "git -C "+armed+" status", true)
		expectCodeExec(t, "git -C "+armed+" commit -m x", true)
		expectCodeExec(t, "git -C "+plain+" status", false)
		expectCodeExec(t, "git -C ../armed status", true)
	})
	t.Run("-C plain from armed cwd", func(t *testing.T) {
		t.Chdir(armed)
		expectCodeExec(t, "git status", true)
		expectCodeExec(t, "git -C "+plain+" status", false)
		expectCodeExec(t, "git -C "+plain+" commit -m x", false)
		expectCodeExec(t, "git -C ../plain status", false)
	})
	t.Run("chained -C", func(t *testing.T) {
		t.Chdir(root)
		expectCodeExec(t, "git -C armed status", true)
		expectCodeExec(t, "git -C plain -C ../armed status", true)
		expectCodeExec(t, "git -C armed -C ../plain status", false)
	})
	t.Run("cd then git", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "cd "+armed+" && git status", true)
		expectCodeExec(t, "cd "+armed+" && git commit -m x", true)
		expectCodeExec(t, "cd ../armed && git status", true)
		t.Chdir(armed)
		expectCodeExec(t, "cd "+plain+" && git status", false)
		expectCodeExec(t, "cd "+plain+" && git commit -m x", false)
		expectCodeExec(t, "cd ../plain && git add . && git commit -m x", false)
		// The effect is tied to the stage, not leaked to earlier commands.
		expectCodeExec(t, "git status && cd "+plain, true)
	})
	t.Run("subshell and wrappers", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "env -C "+armed+" git status", true)
		expectCodeExec(t, "timeout 5 git status", false)
		t.Chdir(armed)
		expectCodeExec(t, "env -C "+plain+" git status", false)
		expectCodeExec(t, "git status | cat", true)
		t.Chdir(plain)
		expectCodeExec(t, "git status | cat", false)
		expectCodeExec(t, "git diff | head", false)
	})
	t.Run("--git-dir", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "git --git-dir="+filepath.Join(armed, ".git")+" status", true)
		expectCodeExec(t, "git --git-dir "+filepath.Join(armed, ".git")+" status", true)
		expectCodeExec(t, "git --git-dir="+filepath.Join(plain, ".git")+" status", false)
		// Retargeting stays a system_write path hijack either way.
		if !hasEffect("git --git-dir="+filepath.Join(plain, ".git")+" status", SystemWrite) {
			t.Errorf("--git-dir lost its system_write escalation")
		}
		t.Chdir(armed)
		expectCodeExec(t, "git --git-dir="+filepath.Join(plain, ".git")+" --work-tree="+plain+" status", false)
	})
	t.Run("walk up from a subdirectory", func(t *testing.T) {
		deep := filepath.Join(armed, "a", "b")
		os.MkdirAll(deep, 0o755)
		t.Chdir(deep)
		expectCodeExec(t, "git status", true)
		deepPlain := filepath.Join(plain, "a", "b")
		os.MkdirAll(deepPlain, 0o755)
		t.Chdir(deepPlain)
		expectCodeExec(t, "git status", false)
	})
	t.Run("inside the git directory", func(t *testing.T) {
		t.Chdir(filepath.Join(armed, ".git", "hooks"))
		expectCodeExec(t, "git status", true)
		t.Chdir(filepath.Join(plain, ".git", "hooks"))
		expectCodeExec(t, "git status", false)
	})
	t.Run("bare repository", func(t *testing.T) {
		bare := filepath.Join(root, "bare.git")
		for _, d := range []string{"hooks", "objects", "refs"} {
			os.MkdirAll(filepath.Join(bare, d), 0o755)
		}
		writeTestFile(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n", 0o644)
		t.Chdir(bare)
		expectCodeExec(t, "git gc", false)
		writeTestFile(t, filepath.Join(bare, "hooks", "pre-auto-gc"), "#!/bin/sh\n", 0o755)
		expectCodeExec(t, "git gc", true)
	})
	t.Run("symlinked directory", func(t *testing.T) {
		link := filepath.Join(root, "link-to-armed")
		if err := os.Symlink(armed, link); err != nil {
			t.Skip("symlinks unavailable")
		}
		t.Chdir(plain)
		expectCodeExec(t, "git -C "+link+" status", true)
	})
}

func TestGitHooksAware_FailsClosed(t *testing.T) {
	isolateGitEnv(t)
	root := t.TempDir()
	plain := makeRepo(t, filepath.Join(root, "plain"), "")

	t.Run("no repository found", func(t *testing.T) {
		bare := filepath.Join(root, "norepo")
		os.MkdirAll(bare, 0o755)
		t.Chdir(bare)
		for _, cmd := range []string{"git status", "git add .", "git commit -m x", "git diff", "git merge x", "git gc"} {
			expectCodeExec(t, cmd, true)
		}
		// History viewers never needed a repository to be routine.
		expectCodeExec(t, "git log", false)
		expectCodeExec(t, "git show HEAD", false)
	})
	t.Run("uncertain cwd", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "cd \"$TARGET\" && git status", true)
		expectCodeExec(t, "cd /nonexistent-odek-dir && git status", true)
		expectCodeExec(t, "cd "+plain+" || cd /elsewhere; git status", true)
		expectCodeExec(t, "cd $(mktemp -d) && git commit -m x", true)
		expectCodeExec(t, "git -C \"$X\" status", true)
		expectCodeExec(t, "git -C ../nonexistent status", true)
		// An absolute -C does not depend on the uncertain cwd.
		expectCodeExec(t, "cd \"$TARGET\" && git -C "+plain+" status", false)
	})
	t.Run("GIT_ assignment", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "GIT_DIR="+filepath.Join(plain, ".git")+" git status", true)
		expectCodeExec(t, "GIT_WORK_TREE="+plain+" git status", true)
		expectCodeExec(t, "GIT_CONFIG_GLOBAL=/tmp/x git status", true)
		expectCodeExec(t, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.hooksPath GIT_CONFIG_VALUE_0=/x git commit -m x", true)
		expectCodeExec(t, "env GIT_DIR=x git status", true)
		expectCodeExec(t, "export GIT_DIR=/x; git status", true)
		expectCodeExec(t, "GIT_EXTERNAL_DIFF=/tmp/x git diff", true)
		// Unrelated assignments do not.
		expectCodeExec(t, "FOO=1 git status", false)
	})
	t.Run("privilege wrappers", func(t *testing.T) {
		t.Chdir(plain)
		expectCodeExec(t, "sudo git status", true)
	})
	t.Run("hooks not a directory", func(t *testing.T) {
		repo := makeRepo(t, filepath.Join(root, "hookfile"), "")
		os.RemoveAll(filepath.Join(repo, ".git", "hooks"))
		writeTestFile(t, filepath.Join(repo, ".git", "hooks"), "x", 0o644)
		t.Chdir(repo)
		expectCodeExec(t, "git commit -m x", true)
	})
	t.Run("config is a directory", func(t *testing.T) {
		repo := makeRepo(t, filepath.Join(root, "cfgdir"), "")
		os.Remove(filepath.Join(repo, ".git", "config"))
		os.MkdirAll(filepath.Join(repo, ".git", "config"), 0o755)
		t.Chdir(repo)
		expectCodeExec(t, "git status", true)
	})
	t.Run("oversized config", func(t *testing.T) {
		repo := makeRepo(t, filepath.Join(root, "bigcfg"), "")
		writeTestFile(t, filepath.Join(repo, ".git", "config"), "[user]\n"+strings.Repeat("# padding padding padding\n", 50000), 0o644)
		t.Chdir(repo)
		expectCodeExec(t, "git status", true)
	})
	t.Run("malformed config", func(t *testing.T) {
		for name, body := range map[string]string{
			"unterminated header": "[core\n\tfsmonitor = false\n",
			"key outside section": "fsmonitor = false\n",
			"empty key":           "[core]\n\t= x\n",
		} {
			repo := makeRepo(t, filepath.Join(root, "bad-"+strings.ReplaceAll(name, " ", "-")), "")
			writeTestFile(t, filepath.Join(repo, ".git", "config"), body, 0o644)
			t.Chdir(repo)
			expectCodeExec(t, "git status", true)
		}
	})
	t.Run("gitfile to nowhere", func(t *testing.T) {
		dir := filepath.Join(root, "dangling")
		writeTestFile(t, filepath.Join(dir, ".git"), "gitdir: /nonexistent/odek/gitdir\n", 0o644)
		t.Chdir(dir)
		expectCodeExec(t, "git status", true)
		writeTestFile(t, filepath.Join(dir, ".git"), "garbage\n", 0o644)
		expectCodeExec(t, "git status", true)
	})
	t.Run("broken commondir", func(t *testing.T) {
		g := filepath.Join(root, "bc", ".git", "worktrees", "w")
		writeTestFile(t, filepath.Join(g, "commondir"), "../../nonexistent\n", 0o644)
		dir := filepath.Join(root, "bc-wt")
		writeTestFile(t, filepath.Join(dir, ".git"), "gitdir: "+g+"\n", 0o644)
		t.Chdir(dir)
		expectCodeExec(t, "git status", true)
	})
	t.Run("unreadable global config", func(t *testing.T) {
		home := isolateGitEnv(t)
		os.MkdirAll(filepath.Join(home, ".gitconfig"), 0o755) // a directory, not a file
		t.Chdir(plain)
		expectCodeExec(t, "git status", true)
	})
	t.Run("no home", func(t *testing.T) {
		isolateGitEnv(t)
		t.Setenv("HOME", "")
		os.Unsetenv("HOME")
		t.Chdir(plain)
		expectCodeExec(t, "git status", true)
	})
}

func TestGitHooksAware_UnconditionalEscalations(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	for _, cmd := range []string{
		"git -c core.hooksPath=/tmp/h commit -m x",
		"git -c core.fsmonitor=/tmp/m status",
		"git -c core.pager='sh -c x' log",
		"git -c alias.st='!sh -c x' st",
		"git -c diff.external=/tmp/d diff",
		"git -c filter.x.clean=/tmp/f add .",
		"git -c merge.m.driver=/tmp/m merge x",
		"git -c core.editor=/tmp/e commit",
		"git -c sequence.editor=/tmp/e rebase -i HEAD~2",
		"git -c include.path=/tmp/evil.cfg status",
		"git -c includeIf.gitdir:/.path=/tmp/evil.cfg status",
		"git -c hook.h.command=/tmp/h commit -m x",
		"git -c interactive.diffFilter=/tmp/f add -p",
		"git -c submodule.s.update='!x' submodule update",
		"git --config-env=core.hooksPath=HP commit -m x",
		"git diff --ext-diff",
		"git diff --textconv",
		"git log -p --textconv",
		"git show --ext-diff HEAD",
		"git diff --ext",
		"git diff --textc",
		"git difftool",
		"git mergetool",
		"git submodule foreach 'make'",
		"git bisect run ./test.sh",
		"git hook run pre-commit",
		"git config core.hooksPath /tmp/h",
		"git config alias.x '!sh'",
		"git rebase -x make main",
		"git rebase --exec 'make test' main",
		"git rebase -i --exec make main",
		"git merge -s custom x",
		"git merge --strategy=custom x",
		"git merge -scustom x",
		"git cherry-pick --strategy custom abc",
		"git --paginate status",
		"git -p log",
		"git --exec-path=/tmp/x status",
		"git commit --no-verify -m x && git config core.hooksPath /x",
	} {
		expectCodeExec(t, cmd, true)
	}
	// Builtin strategies are fine.
	for _, cmd := range []string{"git merge -s ours x --no-edit", "git merge -s recursive -X theirs x --no-edit", "git merge --strategy=ort x --no-edit", "git rebase -s ort main", "git cherry-pick -s abc"} {
		expectCodeExec(t, cmd, false)
	}
	// Non-exec -c keys do not escalate on their own.
	for _, cmd := range []string{"git -c user.name=x commit -m y", "git -c color.ui=always status", "git -C . status"} {
		expectCodeExec(t, cmd, false)
	}
	// Explicit negations of the diff programs keep a routine repo routine.
	expectCodeExec(t, "git diff --no-ext-diff --no-textconv", false)
}

func TestGitHooksAware_DiffNegationsDoNotMaskFsmonitor(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "[core]\n\tfsmonitor = fsm\n[diff \"d\"]\n\ttextconv = t\n")
	t.Chdir(repo)
	expectCodeExec(t, "git diff --no-ext-diff --no-textconv", true)
}

func TestGitHooksAware_LogAndShowOnlyConsultTextconv(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "[core]\n\tfsmonitor = fsm\n\thooksPath = h\n[filter \"x\"]\n\tclean = c\n[diff \"d\"]\n\texternal = e\n")
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "post-commit"), "#!/bin/sh\n", 0o755)
	t.Chdir(repo)
	for _, cmd := range []string{"git log", "git log -p", "git show HEAD", "git log --oneline -5"} {
		expectCodeExec(t, cmd, false)
	}
	writeTestFile(t, filepath.Join(repo, ".git", "config"), "[diff \"pdf\"]\n\ttextconv = pdftotext\n", 0o644)
	expectCodeExec(t, "git log -p", true)
	expectCodeExec(t, "git show HEAD", true)
}

func TestGitHooksAware_ChmodUnreadableHooksFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	hooks := filepath.Join(repo, ".git", "hooks")
	if err := os.Chmod(hooks, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(hooks, 0o755) })
	t.Chdir(repo)
	expectCodeExec(t, "git commit -m x", true)
	// Unreadable config too.
	if err := os.Chmod(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(repo, ".git", "config")
	if err := os.Chmod(cfg, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfg, 0o644) })
	expectCodeExec(t, "git status", true)
}

func TestGitHooksAware_ReadLedgerAndOtherRulesUntouched(t *testing.T) {
	isolateGitEnv(t)
	repo := makeRepo(t, t.TempDir(), "")
	t.Chdir(repo)
	writeTestFile(t, filepath.Join(repo, "msg.txt"), "message\n", 0o644)
	// A message file is data, not an executed script.
	if got := Classify("git commit -F msg.txt"); got != Safe {
		t.Errorf("Classify(git commit -F msg.txt) = %s, want safe", got)
	}
	// Chained mutation of the repository or a remote keeps its own class.
	if got := Classify("git add . && git commit -m x && git push origin main"); got != NetworkEgress {
		t.Errorf("add+commit+push = %s, want network_egress", got)
	}
	if got := Classify("git status && git clean -fdx"); got != SystemWrite {
		t.Errorf("status+clean = %s, want system_write", got)
	}
	// A hook installed in the same command line is a persistence write, and
	// the commit that follows still runs it.
	cmd := "printf '#!/bin/sh\\n' > .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit && git commit -m x"
	if got := Classify(cmd); got != Persistence && got != SystemWrite && got != CodeExecution {
		t.Errorf("hook install = %s, want an escalated class", got)
	}
}

func lookGit() (string, error) { return exec.LookPath("git") }

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
