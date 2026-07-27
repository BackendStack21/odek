package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

// ── NonReusableReason ─────────────────────────────────────────────────

func TestNonReusableReason_AbsoluteUserPath(t *testing.T) {
	bodies := []string{
		"## Overview\n\nUser corrected the approach for cd.\n\n## Step-by-Step\n\n1. cd /Users/kyberneees/Work/github/21no.de/bodek && git tag\n\n## Common Pitfalls\n\n- none",
		"## Overview\n\n1. cd /home/rolando/project && make\n\n## Common Pitfalls\n\n- none",
		`## Overview\n\n1. cd C:\Users\rolando\project && build\n\n## Common Pitfalls` + "\n\n- none",
	}
	for _, body := range bodies {
		s := SkillSuggestion{Name: "x", Body: body}
		if reason := NonReusableReason(s); reason == "" {
			t.Errorf("expected non-reusable reason for body containing absolute user path, got empty")
		}
	}
}

func TestNonReusableReason_AbsolutePathInCommandLog(t *testing.T) {
	s := SkillSuggestion{
		Name:       "x",
		Body:       "## Overview\n\nGeneric body.\n\n## Common Pitfalls\n\n- none",
		CommandLog: []string{"cd /Users/kyberneees/project && go build ./..."},
	}
	if reason := NonReusableReason(s); reason == "" {
		t.Error("expected non-reusable reason for command log with absolute user path")
	}
}

func TestNonReusableReason_MachineSpecificOnly(t *testing.T) {
	// Version tags and project-relative scripts are project-specific, not
	// machine-specific — they no longer trip NonReusableReason.
	s := SkillSuggestion{
		Name: "x",
		Body: "## Overview\n\nRelease flow.\n\n## Step-by-Step\n\n1. ./scripts/release.sh && git tag -a v0.0.14 -m \"release\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- none",
	}
	if reason := NonReusableReason(s); reason != "" {
		t.Errorf("expected no machine-specific reason, got %q", reason)
	}
}

// ── ProjectScopeReason ────────────────────────────────────────────────

func TestProjectScopeReason_HardcodedVersionTag(t *testing.T) {
	s := SkillSuggestion{
		Name: "x",
		Body: "## Overview\n\nRelease flow.\n\n## Step-by-Step\n\n1. git tag -a v0.0.14 -m \"release\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- none",
	}
	reason := ProjectScopeReason(s)
	if reason == "" {
		t.Error("expected project-scope reason for hardcoded version tag")
	}
}

func TestProjectScopeReason_ProjectRelativeScript(t *testing.T) {
	bodies := []string{
		"## Overview\n\nDeploy.\n\n## Step-by-Step\n\n1. ./scripts/deploy.sh production\n\n## Common Pitfalls\n\n- none",
		"## Overview\n\nRelease.\n\n## Step-by-Step\n\n1. make build && ./bin/release --dry-run\n\n## Common Pitfalls\n\n- none",
	}
	for _, body := range bodies {
		s := SkillSuggestion{Name: "x", Body: body}
		if reason := ProjectScopeReason(s); reason == "" {
			t.Errorf("expected project-scope reason for body with repo-rooted script, got empty")
		}
	}
}

func TestProjectScopeReason_Reusable(t *testing.T) {
	s := SkillSuggestion{
		Name:       "go-race-test",
		Body:       "## Overview\n\nRun Go tests with the race detector.\n\n## Step-by-Step\n\n1. go test -race ./...\n\n## Common Pitfalls\n\n- Forget -count=1 to bypass cache\n\n## Verification\n\n- Exit code 0",
		CommandLog: []string{"go test -race ./...", "go build ./..."},
	}
	if reason := ProjectScopeReason(s); reason != "" {
		t.Errorf("expected reusable, got reason %q", reason)
	}
}

func TestNonReusableReason_Reusable(t *testing.T) {
	s := SkillSuggestion{
		Name:       "go-race-test",
		Body:       "## Overview\n\nRun Go tests with the race detector.\n\n## Step-by-Step\n\n1. go test -race ./...\n\n## Common Pitfalls\n\n- Forget -count=1 to bypass cache\n\n## Verification\n\n- Exit code 0",
		CommandLog: []string{"go test -race ./...", "go build ./..."},
	}
	if reason := NonReusableReason(s); reason != "" {
		t.Errorf("expected reusable, got reason %q", reason)
	}
}

// ── AutoSave integration ──────────────────────────────────────────────

func TestAutoSaveSuggestions_SkipsNonReusable(t *testing.T) {
	dir := t.TempDir()
	junkBody := "## Overview\n\nUser corrected the approach for cd.\n\n## Step-by-Step\n\n1. cd /Users/kyberneees/Work/github/21no.de/bodek && git tag -a v0.0.14 -m \"fix\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- The initial approach was incorrect\n\n## Verification\n\n- run it"
	cleanBody := "## Overview\n\nRun Go tests with race detector and no cache for reliable results in CI pipelines and local development workflows alike.\n\n## Step-by-Step\n\n1. go test -race -count=1 ./...\n\n## Common Pitfalls\n\n- none\n\n## Verification\n\n- exit 0"

	suggestions := []SkillSuggestion{
		{Name: "corrected-cd", Heuristic: "user-correction", Body: junkBody},
		{Name: "go-race-test", Heuristic: "multi-step", Body: cleanBody},
	}

	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MaxPerRun = 5
	result := AutoSaveSuggestions(suggestions, dir, "", cfg, nil, guard.Config{}, false)

	if len(result.NonReusable) != 1 || result.NonReusable[0] != "corrected-cd" {
		t.Errorf("expected corrected-cd in NonReusable, got %v", result.NonReusable)
	}
	if len(result.Saved) != 1 || result.Saved[0] != "go-race-test" {
		t.Errorf("expected only go-race-test saved, got %v", result.Saved)
	}
}

func TestAutoSaveSuggestions_RedirectsProjectScoped(t *testing.T) {
	userDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), ".odek", "skills")
	projectBody := "## Overview\n\nCut a release for this project using its release script.\n\n## Step-by-Step\n\n1. ./scripts/release.sh && git tag -a v0.0.14 -m \"release\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- Tag must match the chart version\n\n## Verification\n\n- git ls-remote --tags origin"

	suggestions := []SkillSuggestion{
		{Name: "project-release", Heuristic: "multi-step", Body: projectBody},
	}

	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MaxPerRun = 5
	result := AutoSaveSuggestions(suggestions, userDir, projectDir, cfg, nil, guard.Config{}, false)

	if len(result.ProjectSaved) != 1 || result.ProjectSaved[0] != "project-release" {
		t.Fatalf("expected project-release in ProjectSaved, got %v (saved=%v nonreusable=%v failed=%v)",
			result.ProjectSaved, result.Saved, result.NonReusable, result.Failed)
	}
	if len(result.Saved) != 0 {
		t.Errorf("project-scoped skill must not be saved globally, got %v", result.Saved)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "project-release", "SKILL.md")); err != nil {
		t.Errorf("expected skill file under project dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userDir, "project-release")); !os.IsNotExist(err) {
		t.Errorf("project-scoped skill leaked into global dir")
	}

	// A skill scanned back from the project dir is pinned to NeedsReview,
	// so the redirect can never smuggle content into auto-load.
	res := ScanDirs(projectDir, userDir, nil)
	for _, sk := range append(append([]Skill{}, res.AutoLoad...), res.Lazy...) {
		if sk.Name == "project-release" && !sk.Provenance.NeedsReview {
			t.Errorf("redirected project skill must be NeedsReview after scan")
		}
	}
}

func TestAutoSaveSuggestions_DropsProjectScopedWhenNoProjectDir(t *testing.T) {
	userDir := t.TempDir()
	projectBody := "## Overview\n\nCut a release for this project using its release script.\n\n## Step-by-Step\n\n1. ./scripts/release.sh && git tag -a v0.0.14 -m \"release\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- Tag must match the chart version\n\n## Verification\n\n- git ls-remote --tags origin"
	suggestions := []SkillSuggestion{
		{Name: "project-release", Heuristic: "multi-step", Body: projectBody},
	}

	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MaxPerRun = 5

	// No project dir at all.
	result := AutoSaveSuggestions(suggestions, userDir, "", cfg, nil, guard.Config{}, false)
	if len(result.NonReusable) != 1 {
		t.Errorf("expected drop when projectDir empty, got %+v", result)
	}

	// Project dir resolving to the same location as the global dir
	// (the odek-run-from-$HOME case) must also drop, not save.
	result = AutoSaveSuggestions(suggestions, userDir, userDir, cfg, nil, guard.Config{}, false)
	if len(result.NonReusable) != 1 || len(result.Saved) != 0 || len(result.ProjectSaved) != 0 {
		t.Errorf("expected drop when projectDir == userDir, got %+v", result)
	}
}

// ── DetectCorrection tightening ───────────────────────────────────────

func TestDetectCorrection_GenericWordsNoLongerTrigger(t *testing.T) {
	calls := []ToolCall{
		{Tool: "shell", Input: "git status", ExitCode: 0, Turn: 0},
		{Tool: "shell", Input: "git log --oneline -3", ExitCode: 0, Turn: 1},
		{Tool: "shell", Input: "git diff", ExitCode: 0, Turn: 2},
	}
	for _, msg := range []string{"no", "try again", "that is a different question"} {
		if got := DetectCorrection(calls, []string{msg}); len(got) != 0 {
			t.Errorf("DetectCorrection with generic message %q should not fire, got %v", msg, got)
		}
	}
}

func TestDetectCorrection_UnrelatedCommandsRejected(t *testing.T) {
	// Explicit correction phrase, but the two trailing commands are
	// unrelated one-off operations (different lead verbs) — the exact
	// shape that produced the garbage `corrected-cd` skill.
	msgs := []string{"no, use git tag instead of the merge workflow"}
	calls := []ToolCall{
		{Tool: "shell", Input: "gh pr merge 15 --merge --delete-branch", ExitCode: 0, Turn: 0},
		{Tool: "shell", Input: "cd /tmp/project && git tag -a v0.0.14 -m \"fix\" && git push origin v0.0.14", ExitCode: 0, Turn: 1},
	}
	if got := DetectCorrection(calls, msgs); len(got) != 0 {
		t.Errorf("expected no suggestion for unrelated command pair, got %v", got)
	}
}

func TestDetectCorrection_RelatedCommandsAccepted(t *testing.T) {
	msgs := []string{"no, use --onto instead for the rebase"}
	calls := []ToolCall{
		{Tool: "shell", Input: "git fetch origin", ExitCode: 0, Turn: 0},
		{Tool: "shell", Input: "git rebase main", ExitCode: 0, Turn: 1},
		{Tool: "shell", Input: "git rebase --onto main feature", ExitCode: 0, Turn: 2},
	}
	got := DetectCorrection(calls, msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(got))
	}
	if got[0].Name != "corrected-git" {
		t.Errorf("Name = %q, want corrected-git", got[0].Name)
	}
}

// ── extractTopic plumbing skip ────────────────────────────────────────

func TestExtractTopic_SkipsShellPlumbing(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"cd /Users/x/project && git tag v1", "git"},
		{"sudo systemctl restart nginx", "systemctl"},
		{"cd .. && npm test", "npm"},
		{"docker build .", "docker"},
	}
	for _, tt := range tests {
		if got := extractTopic(tt.cmd); got != tt.want {
			t.Errorf("extractTopic(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

// ── DeriveKeywords hygiene ────────────────────────────────────────────

func TestDeriveKeywords_FiltersShellGarbage(t *testing.T) {
	body := "## Overview\n\nUser corrected the approach for cd.\n\n## Step-by-Step\n\n1. cd project && git tag -a v0.0.14 -m \"fix\" && git push origin v0.0.14\n\n## Common Pitfalls\n\n- The initial approach was incorrect\n\n## Verification\n\n- git tag --points-at HEAD"
	topics, _ := DeriveKeywords(body)
	for _, kw := range topics {
		if kw == "&&" || kw == "##" || kw == "-m" || kw == "-a" || kw == "v0" || kw == "14" || kw == "0" {
			t.Errorf("shell garbage %q leaked into topic keywords: %v", kw, topics)
		}
		if strings.HasPrefix(kw, "-") {
			t.Errorf("flag %q leaked into topic keywords: %v", kw, topics)
		}
	}
}
