package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCurateSkills_Staleness(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -100) // 100 days ago

	skills := []Skill{
		{
			Name:     "stale-skill",
			Quality:  QualityDraft,
			LastUsed: old,
			Body:     "## Overview\n\nX\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
			Trigger:  SkillTrigger{TopicKeywords: []string{"test"}},
		},
		{
			Name:     "fresh-skill",
			Quality:  QualityVerified,
			LastUsed: now,
			Body:     "## Overview\n\nX\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
			Trigger:  SkillTrigger{TopicKeywords: []string{"test"}},
		},
	}

	report := CurateSkills(skills, CurateOptions{StalenessDays: 90})
	if len(report.StaleSkills) != 1 {
		t.Fatalf("expected 1 stale skill, got %d", len(report.StaleSkills))
	}
	if report.StaleSkills[0].Name != "stale-skill" {
		t.Errorf("expected stale-skill, got %q", report.StaleSkills[0].Name)
	}
	if report.TotalSkills != 2 {
		t.Errorf("TotalSkills = %d, want 2", report.TotalSkills)
	}
}

func TestCurateSkills_Overlap(t *testing.T) {
	skills := []Skill{
		{
			Name:    "docker-build",
			Trigger: SkillTrigger{TopicKeywords: []string{"docker", "container", "build"}},
			Body:    "## Overview\n\nX\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		},
		{
			Name:    "docker-deploy",
			Trigger: SkillTrigger{TopicKeywords: []string{"docker", "container", "deploy"}},
			Body:    "## Overview\n\nY\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		},
		{
			Name:    "unrelated",
			Trigger: SkillTrigger{TopicKeywords: []string{"go", "golang"}},
			Body:    "## Overview\n\nZ\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		},
	}

	report := CurateSkills(skills, CurateOptions{})
	if len(report.OverlapGroups) != 1 {
		t.Fatalf("expected 1 overlap group, got %d", len(report.OverlapGroups))
	}
	group := report.OverlapGroups[0]
	if len(group.Skills) != 2 {
		t.Errorf("expected 2 skills in group, got %v", group.Skills)
	}
}

func TestCurateSkills_QualityAudit(t *testing.T) {
	skills := []Skill{
		{
			Name:    "no-pitfalls",
			Body:    "## Overview\n\nJust an overview, no pitfalls section. This body needs to be long enough to pass the 300 character threshold for the body length check. Adding more text to make sure we are well above the limit. And still more. And even more. This should be enough now. Yes, definitely over 300 chars now, just a bit more to be safe.",
			Trigger: SkillTrigger{TopicKeywords: []string{"test"}},
		},
		{
			Name:    "complete",
			Body:    "## Overview\n\nGood\n\n## Common Pitfalls\n\nCovered\n\n## Verification\n\nCheck. This body needs to be long enough to pass the 300 char threshold. Adding more text. And more text. And even more. Still more. Almost there. Just a bit more now. Yes, this should be over 300 characters now. Adding final padding to be absolutely safe for the validation threshold.",
			Trigger: SkillTrigger{TopicKeywords: []string{"test"}},
		},
	}

	report := CurateSkills(skills, CurateOptions{})
	t.Logf("quality issues: %+v", report.QualityIssues)
	if len(report.QualityIssues) != 1 {
		t.Fatalf("expected 1 quality issue, got %d", len(report.QualityIssues))
	}
	if report.QualityIssues[0].Name != "no-pitfalls" {
		t.Errorf("expected no-pitfalls, got %q", report.QualityIssues[0].Name)
	}
}

func TestCurateSkills_Dedup(t *testing.T) {
	body := "## Overview\n\nDuplicated content\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check"
	skills := []Skill{
		{Name: "a", Body: body, BodyHash: HashBody(body), Trigger: SkillTrigger{TopicKeywords: []string{"test"}}},
		{Name: "b", Body: body, BodyHash: HashBody(body), Trigger: SkillTrigger{TopicKeywords: []string{"test"}}},
	}

	report := CurateSkills(skills, CurateOptions{})
	if report.Deduplicated != 1 {
		t.Errorf("expected 1 dedup group, got %d", report.Deduplicated)
	}
}

func TestAuditQuality_MissingSections(t *testing.T) {
	issues := auditQuality(Skill{
		Name: "test",
		Body: "No real sections",
	})
	if len(issues) == 0 {
		t.Error("expected issues for skill with no sections")
	}
}

func TestAuditQuality_Complete(t *testing.T) {
	issues := auditQuality(Skill{
		Name:        "test",
		Description: "Short desc",
		Body:        "## Overview\n\nX\n\n## Common Pitfalls\n\nY\n\n## Verification\n\nZ\nLong enough body here to pass the 300 char threshold. Adding more text to make sure we pass. And still more text. And even more text. And even more. This should be enough now. Yes definitely over 300 chars. Just a little more to be absolutely safe for the threshold.",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker"}},
	})
	if len(issues) != 0 {
		t.Errorf("expected no issues, got: %v", issues)
	}
}

func TestIntersectKeywords(t *testing.T) {
	a := []string{"docker", "container", "build"}
	b := []string{"docker", "deploy", "container"}
	result := intersectKeywords(a, b)
	if len(result) != 2 {
		t.Errorf("expected 2 intersections, got %v", result)
	}
}

func TestMicroCuration_NoDuplicates(t *testing.T) {
	result := MicroCuration("", nil, nil, CurationConfig{StalenessDays: 90})
	if result == nil {
		t.Error("expected non-nil result")
	}
	if len(result.Merged) > 0 || len(result.Flagged) > 0 {
		t.Errorf("expected empty result, got merged=%v flagged=%v", result.Merged, result.Flagged)
	}
}

func TestFormatCurationReport(t *testing.T) {
	report := &CurationReport{
		TotalSkills: 10,
		StaleSkills: []Skill{{Name: "old"}},
		QualityIssues: []QualityIssue{
			{Name: "bad", Issues: []string{"missing section"}},
		},
	}
	output := FormatCurationReport(report)
	if !contains(output, "10") {
		t.Errorf("expected total count in output: %s", output)
	}
	if !contains(output, "old") {
		t.Errorf("expected stale skill name: %s", output)
	}
	if !contains(output, "bad") {
		t.Errorf("expected quality issue name: %s", output)
	}
}

func TestMergeSkills_Basic(t *testing.T) {
	a := Skill{
		Name:        "skill-a",
		Description: "First skill",
		Body:        "## Overview\n\nContent A\n\n## Common Pitfalls\n\n- Pitfall A\n\n## Verification\n\n- Check A",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker", "build"}, ActionKeywords: []string{"run"}},
		Quality:     QualityDraft,
	}
	b := Skill{
		Name:        "skill-b",
		Description: "Second skill",
		Body:        "## Overview\n\nContent B\n\n## Common Pitfalls\n\n- Pitfall B\n\n## Verification\n\n- Check B",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker", "deploy"}, ActionKeywords: []string{"test"}},
		Quality:     QualityDraft,
	}

	merged := MergeSkills(a, b)

	// Body should contain both contents
	if !contains(merged.Body, "Content A") || !contains(merged.Body, "Content B") {
		t.Errorf("merged body missing content: %s", merged.Body)
	}
	if !contains(merged.Body, "Merged from skill-b") {
		t.Errorf("merged body missing merge marker: %s", merged.Body)
	}

	// Keywords should be unioned
	if len(merged.Trigger.TopicKeywords) < 3 {
		t.Errorf("expected >=3 topic keywords, got %v", merged.Trigger.TopicKeywords)
	}
	if len(merged.Trigger.ActionKeywords) < 2 {
		t.Errorf("expected >=2 action keywords, got %v", merged.Trigger.ActionKeywords)
	}

	// Quality should be draft after merge
	if merged.Quality != QualityDraft {
		t.Errorf("merged Quality = %q, want draft", merged.Quality)
	}

	// BodyHash should be updated
	if merged.BodyHash == "" || merged.BodyHash == a.BodyHash {
		t.Error("BodyHash should be recalculated after merge")
	}
}

func TestMergeSkills_NameOrder(t *testing.T) {
	// When names are in wrong order, MergeSkills should still work
	// (caller sorts alphabetically)
	a := Skill{
		Name:    "zzz-later",
		Body:    "## Overview\n\nLater\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check later",
		Trigger: SkillTrigger{TopicKeywords: []string{"zzz"}},
	}
	b := Skill{
		Name:    "aaa-first",
		Body:    "## Overview\n\nFirst\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check first",
		Trigger: SkillTrigger{TopicKeywords: []string{"aaa"}},
	}

	// Merge with a as keep (wrong order)
	merged := MergeSkills(a, b)
	if !contains(merged.Body, "Merged from aaa-first") {
		t.Errorf("should merge b into a: %s", merged.Body)
	}
}

func TestMergeSkills_ProvenanceKeepsWorse(t *testing.T) {
	clean := Skill{
		Name:    "clean",
		Body:    "## Overview\n\nClean\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		Trigger: SkillTrigger{TopicKeywords: []string{"clean"}},
	}
	tainted := Skill{
		Name:    "tainted",
		Body:    "## Overview\n\nTainted\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		Trigger: SkillTrigger{TopicKeywords: []string{"tainted"}},
		Provenance: SkillProvenance{
			Untrusted:   true,
			NeedsReview: true,
			Sources:     []string{"browser:https://example.com", "session:abc"},
		},
	}

	// Tainted merged INTO clean: the merged body contains tainted content, so
	// the result must inherit the worse provenance, not the keeper's clean one.
	merged := MergeSkills(clean, tainted)
	if !merged.Provenance.Untrusted {
		t.Error("merged provenance should be Untrusted when either input is")
	}
	if !merged.Provenance.NeedsReview {
		t.Error("merged provenance should be NeedsReview when either input is")
	}
	wantSources := []string{"browser:https://example.com", "session:abc"}
	if len(merged.Provenance.Sources) != len(wantSources) {
		t.Fatalf("merged Sources = %v, want %v", merged.Provenance.Sources, wantSources)
	}
	for i, s := range wantSources {
		if merged.Provenance.Sources[i] != s {
			t.Errorf("merged Sources[%d] = %q, want %q", i, merged.Provenance.Sources[i], s)
		}
	}

	// Sources are unioned deduped and order-stable (keeper's first).
	keep := Skill{
		Name:    "keep",
		Body:    "## Overview\n\nKeep\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		Trigger: SkillTrigger{TopicKeywords: []string{"keep"}},
		Provenance: SkillProvenance{
			Untrusted:   true,
			NeedsReview: true,
			Sources:     []string{"session:abc", "browser:https://a.dev"},
		},
	}
	merged = MergeSkills(keep, tainted)
	want := []string{"session:abc", "browser:https://a.dev", "browser:https://example.com"}
	if len(merged.Provenance.Sources) != len(want) {
		t.Fatalf("merged Sources = %v, want %v", merged.Provenance.Sources, want)
	}
	for i, s := range want {
		if merged.Provenance.Sources[i] != s {
			t.Errorf("merged Sources[%d] = %q, want %q", i, merged.Provenance.Sources[i], s)
		}
	}
}

func TestMergeSkills_ProvenanceCleanStaysClean(t *testing.T) {
	a := Skill{
		Name:    "a",
		Body:    "## Overview\n\nA\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		Trigger: SkillTrigger{TopicKeywords: []string{"a"}},
	}
	b := Skill{
		Name:    "b",
		Body:    "## Overview\n\nB\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check",
		Trigger: SkillTrigger{TopicKeywords: []string{"b"}},
	}

	merged := MergeSkills(a, b)
	if merged.Provenance.Untrusted {
		t.Error("clean+clean merge should stay trusted")
	}
	if merged.Provenance.NeedsReview {
		t.Error("clean+clean merge should not need review")
	}
	if len(merged.Provenance.Sources) != 0 {
		t.Errorf("clean+clean merge should have no sources, got %v", merged.Provenance.Sources)
	}
}

func TestExecuteMicroCuration_Merge(t *testing.T) {
	dir := t.TempDir()

	// Create two skills
	a := Skill{
		Name:        "skill-keep",
		Description: "Keep me",
		Body:        "## Overview\n\nKeep body text that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- Keep pitfall\n\n## Verification\n\n- Check keep. Adding more text to reach the minimum body length threshold for validation. More text still needed. OK this should be enough now.",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker"}, ActionKeywords: []string{"build"}},
		Quality:     QualityDraft,
	}
	b := Skill{
		Name:        "skill-remove",
		Description: "Remove me",
		Body:        "## Overview\n\nRemove body text that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- Remove pitfall\n\n## Verification\n\n- Check remove. Adding more text to reach the minimum body length threshold for validation. More text still needed. OK this should be enough now.",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker"}, ActionKeywords: []string{"push"}},
		Quality:     QualityDraft,
	}

	if err := WriteSkill(dir, a); err != nil {
		t.Fatal(err)
	}
	if err := WriteSkill(dir, b); err != nil {
		t.Fatal(err)
	}

	result := &MicroCurationResult{
		Merged: []string{"skill-keep", "skill-remove"},
		Notes:  []string{"test merge"},
	}

	if err := ExecuteMicroCuration(dir, result, []Skill{a, b}); err != nil {
		t.Fatal(err)
	}

	// skill-keep should still exist
	if _, err := os.Stat(filepath.Join(dir, "skill-keep", "SKILL.md")); err != nil {
		t.Error("skill-keep should still exist")
	}
	// skill-remove should be deleted
	if _, err := os.Stat(filepath.Join(dir, "skill-remove")); !os.IsNotExist(err) {
		t.Error("skill-remove should be deleted")
	}
}

func TestExecuteMicroCuration_Delete(t *testing.T) {
	dir := t.TempDir()

	s := Skill{
		Name:    "delete-me",
		Body:    "## Overview\n\nDelete body text that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- None\n\n## Verification\n\n- Check. Adding more text to reach the minimum body length threshold for validation.",
		Trigger: SkillTrigger{TopicKeywords: []string{"test"}},
	}
	if err := WriteSkill(dir, s); err != nil {
		t.Fatal(err)
	}

	result := &MicroCurationResult{
		Deleted: []string{"delete-me"},
		Notes:   []string{"test delete"},
	}

	if err := ExecuteMicroCuration(dir, result, []Skill{s}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "delete-me")); !os.IsNotExist(err) {
		t.Error("skill should be deleted")
	}
}

func TestMicroCuration_OverlapGroups(t *testing.T) {
	dir := t.TempDir()
	body := "## Overview\n\nTest body that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- Test pitfall\n\n## Verification\n\n- Test verification. Adding more text to reach the minimum body length threshold for validation. More text still needed. OK this should be enough."

	a := Skill{
		Name:    "skill-x",
		Body:    body,
		Trigger: SkillTrigger{TopicKeywords: []string{"docker", "container", "build"}},
		Quality: QualityDraft,
	}
	b := Skill{
		Name:    "skill-y",
		Body:    body + " different",
		Trigger: SkillTrigger{TopicKeywords: []string{"docker", "container", "deploy"}},
		Quality: QualityDraft,
	}

	result := MicroCuration(dir, nil, []Skill{a, b}, CurationConfig{StalenessDays: 90})
	if len(result.Merged) < 2 {
		t.Errorf("expected overlap merge, got merged=%v", result.Merged)
	}
}

func TestRunAutoCurate_SkipsDeleted(t *testing.T) {
	dir := t.TempDir()

	// Create a skill
	body := "## Overview\n\nTest body that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- Test pitfall\n\n## Verification\n\n- Test verification. Adding more text to reach the minimum body length threshold for validation. More text still needed. OK this should be enough."

	s := Skill{
		Name:    "skipped-3-times",
		Body:    body,
		Trigger: SkillTrigger{TopicKeywords: []string{"test"}},
		Quality: QualityDraft,
	}
	if err := WriteSkill(dir, s); err != nil {
		t.Fatal(err)
	}

	// Record 3 skips (threshold is 1 in default config)
	sl := LoadSkipList(dir)
	for i := 0; i < 3; i++ {
		sl.RecordSkip(dir, "skipped-3-times", "multi-step")
	}

	cfg := DefaultSkillsConfig()
	cfg.Curation.SkipThreshold = 3
	cfg.Curation.AutoPrune = true

	msg := RunAutoCurate(dir, nil, []Skill{s}, cfg, nil)
	if !contains(msg, "skipped-3-times") {
		t.Errorf("expected skip-threshold deletion in output: %s", msg)
	}

	// Skill should be deleted
	if _, err := os.Stat(filepath.Join(dir, "skipped-3-times")); !os.IsNotExist(err) {
		t.Error("skill skipped 3 times should be deleted")
	}
}

func TestKeysFromSet(t *testing.T) {
	set := map[string]bool{"a": true, "b": true, "c": true}
	keys := keysFromSet(set)
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %v", keys)
	}
}

func TestMicroCuration_StaleFlagging(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -100)

	body := "## Overview\n\nTest body that is long enough to pass validation checks. Adding more text here to make sure the body is at least 300 characters long. Still more text needed. Almost there. Just a bit more. Done now yes.\n\n## Common Pitfalls\n\n- Test pitfall\n\n## Verification\n\n- Test verification. Adding more text to reach the minimum body length threshold for validation. More text still needed. OK this should be enough."

	skills := []Skill{
		{Name: "stale-draft", Quality: QualityDraft, LastUsed: old, Body: body, Trigger: SkillTrigger{TopicKeywords: []string{"test"}}},
		{Name: "stale-manual", Quality: QualityManual, LastUsed: old, Body: body, Trigger: SkillTrigger{TopicKeywords: []string{"test"}}},
	}

	result := MicroCuration("", nil, skills, CurationConfig{StalenessDays: 90, AutoCurate: true})

	// stale-draft should be flagged (draft, old)
	foundDraft := false
	for _, name := range result.Flagged {
		if name == "stale-draft" {
			foundDraft = true
		}
	}
	if !foundDraft {
		t.Errorf("expected stale-draft to be flagged, got %v", result.Flagged)
	}

	// stale-manual should NOT be flagged (manual quality preserved)
	for _, name := range result.Flagged {
		if name == "stale-manual" {
			t.Error("manual skills should not be flagged as stale")
		}
	}
}
