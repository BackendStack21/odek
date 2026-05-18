package skills

import (
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
		Trigger: SkillTrigger{TopicKeywords: []string{"docker"}},
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
	msg := MicroCuration("", nil, nil)
	if msg != "" {
		t.Errorf("expected empty, got %q", msg)
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
