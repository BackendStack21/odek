package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkillFile writes a SKILL.md under dir/<name>/SKILL.md.
func writeSkillFile(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\n" + frontmatter + "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestScanDirs_NeedsReviewSkillsLanIn Lazy verifies the provenance gate:
// a skill with auto_load=true but needs_review=true must NOT appear in
// AutoLoad. This is what stops a poisoned auto-saved skill from
// activating on the next session.
func TestScanDirs_NeedsReviewSkillsLandInLazy(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "clean-skill",
		"name: clean-skill\ndescription: clean\nodek:\n  auto_load: true\n",
		"## Overview\nclean body\n## Common Pitfalls\nnone\n")
	writeSkillFile(t, dir, "tainted-skill",
		"name: tainted-skill\ndescription: tainted\nodek:\n  auto_load: true\n  provenance:\n    untrusted: true\n    needs_review: true\n",
		"## Overview\ntainted body\n## Common Pitfalls\nnone\n")

	res := ScanDirs("", dir, nil)
	if res == nil {
		t.Fatal("ScanDirs returned nil")
	}

	var sawCleanAuto, sawTaintedAuto, sawTaintedLazy bool
	for _, s := range res.AutoLoad {
		if s.Name == "clean-skill" {
			sawCleanAuto = true
		}
		if s.Name == "tainted-skill" {
			sawTaintedAuto = true
		}
	}
	for _, s := range res.Lazy {
		if s.Name == "tainted-skill" {
			sawTaintedLazy = true
		}
	}
	if !sawCleanAuto {
		t.Error("clean skill missing from AutoLoad")
	}
	if sawTaintedAuto {
		t.Error("tainted needs-review skill was placed in AutoLoad — provenance gate failed")
	}
	if !sawTaintedLazy {
		t.Error("tainted needs-review skill missing from Lazy fallback")
	}
}

// TestScanDirs_PromotedSkillLandsInAutoLoad confirms the inverse: a
// skill that had Untrusted=true but the user cleared NeedsReview (via
// `odek skill promote`) IS auto-loaded.
func TestScanDirs_PromotedSkillLandsInAutoLoad(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "promoted-skill",
		"name: promoted-skill\ndescription: promoted\nodek:\n  auto_load: true\n  provenance:\n    sources: browser\n",
		"## Overview\nbody\n## Common Pitfalls\nnone\n")

	res := ScanDirs("", dir, nil)
	if res == nil {
		t.Fatal("ScanDirs returned nil")
	}
	var sawAuto bool
	for _, s := range res.AutoLoad {
		if s.Name == "promoted-skill" {
			sawAuto = true
			break
		}
	}
	if !sawAuto {
		t.Error("promoted (NeedsReview=false) skill missing from AutoLoad")
	}
}

// TestScanDirs_ProjectDirSkillsDistrusted verifies that skills from the
// project-local dir are pinned to NeedsReview (and out of AutoLoad) even
// when they declare auto_load: true, while operator-controlled user-dir
// skills are unaffected.
func TestScanDirs_ProjectDirSkillsDistrusted(t *testing.T) {
	projectDir := t.TempDir()
	userDir := t.TempDir()
	writeSkillFile(t, projectDir, "proj-skill",
		"name: proj-skill\ndescription: from project\nodek:\n  auto_load: true\n",
		"## Overview\nproject body\n## Common Pitfalls\nnone\n")
	writeSkillFile(t, userDir, "user-skill",
		"name: user-skill\ndescription: from user\nodek:\n  auto_load: true\n",
		"## Overview\nuser body\n## Common Pitfalls\nnone\n")

	res := ScanDirs(projectDir, userDir, nil)
	if res == nil {
		t.Fatal("ScanDirs returned nil")
	}

	var sawUserAuto bool
	for _, s := range res.AutoLoad {
		if s.Name == "proj-skill" {
			t.Error("project-dir auto_load skill must not reach AutoLoad")
		}
		if s.Name == "user-skill" {
			sawUserAuto = true
		}
	}
	if !sawUserAuto {
		t.Error("user-dir auto_load skill missing from AutoLoad")
	}

	var proj *Skill
	for i := range res.Lazy {
		if res.Lazy[i].Name == "proj-skill" {
			proj = &res.Lazy[i]
		}
	}
	if proj == nil {
		t.Fatal("project-dir skill missing from Lazy")
	}
	if !proj.Provenance.NeedsReview {
		t.Error("project-dir skill should have NeedsReview=true")
	}
	var hasProjectSource bool
	for _, src := range proj.Provenance.Sources {
		if src == "project" {
			hasProjectSource = true
		}
	}
	if !hasProjectSource {
		t.Errorf("project-dir skill Sources should include \"project\", got %v", proj.Provenance.Sources)
	}
}

// TestMatchLazySkills_NeedsReviewExcluded verifies that a NeedsReview lazy
// skill is still listed in ScanResult.Lazy but is never trigger-injected;
// after the flag is cleared and the manager reloads, it matches again.
func TestMatchLazySkills_NeedsReviewExcluded(t *testing.T) {
	dir := t.TempDir()
	frontmatter := "name: review-skill\ndescription: needs review\nodek:\n  auto_load: false\n  trigger:\n    topic: frobnicate\n"
	writeSkillFile(t, dir, "review-skill",
		frontmatter+"  provenance:\n    needs_review: true\n",
		"## Overview\nbody\n## Common Pitfalls\nnone\n")

	sm := NewSkillManager(dir, "")

	// Still visible in the Lazy listing (promotion flow can find it).
	listed := false
	for _, s := range sm.Result.Lazy {
		if s.Name == "review-skill" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("NeedsReview skill missing from Lazy listing")
	}

	// But excluded from trigger matching.
	if matched := sm.MatchLazySkills("please frobnicate this", 5); len(matched) != 0 {
		t.Errorf("NeedsReview skill must not be trigger-matched, got %v", skillNames(matched))
	}

	// Promote (clear NeedsReview on disk) and reload — now it matches.
	writeSkillFile(t, dir, "review-skill", frontmatter,
		"## Overview\nbody\n## Common Pitfalls\nnone\n")
	sm.MarkDirty()
	sm.Reload()

	matched := sm.MatchLazySkills("please frobnicate this", 5)
	found := false
	for _, m := range matched {
		if m.Name == "review-skill" {
			found = true
		}
	}
	if !found {
		t.Errorf("promoted skill should be trigger-matched after reload, got %v", skillNames(matched))
	}
}
