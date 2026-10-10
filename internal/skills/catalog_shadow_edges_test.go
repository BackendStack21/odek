package skills

import (
	"strings"
	"testing"
)

func TestScanDirs_LaterNeedsReviewCopyIgnored(t *testing.T) {
	user := t.TempDir()
	extra := t.TempDir()
	writeShadowSkill(t, user, "lint", "lint", "trusted user", "")
	writeShadowSkill(t, extra, "lint", "lint", "pending copy", "odek:\n  provenance:\n    needs_review: true\n")
	got := findSkill(ScanDirs("", user, []string{extra}), "lint")
	if len(got) != 1 || got[0].Description != "trusted user" {
		t.Fatalf("later NeedsReview copy must be ignored: %+v", got)
	}
}

func TestScanDirs_EqualTrustFirstWins(t *testing.T) {
	user := t.TempDir()
	extra := t.TempDir()
	writeShadowSkill(t, user, "b", "build", "user copy", "")
	writeShadowSkill(t, extra, "b", "build", "extra copy", "")
	got := findSkill(ScanDirs("", user, []string{extra}), "build")
	if len(got) != 1 || got[0].Description != "user copy" {
		t.Fatalf("equal-trust collision must keep scan order: %+v", got)
	}
}

func TestValidateSkillName_RejectsHangulFiller(t *testing.T) {
	if err := ValidateSkillName("aㅤb"); err == nil {
		t.Error("invisible Hangul filler accepted in a skill name")
	}
}

func TestFormatCatalog_WithheldCountLine(t *testing.T) {
	list := []Skill{
		{Name: "ok-skill", Description: "fine"},
		{Name: "two words", Provenance: SkillProvenance{NeedsReview: true}},
		{Name: "flagged", NameFlagged: true},
	}
	got := FormatCatalog(list, 0)
	if !strings.Contains(got, "- (2 skill(s) pending review, not listed") {
		t.Errorf("withheld count line missing:\n%s", got)
	}
	if strings.Contains(got, "two words") || strings.Contains(got, "- flagged") {
		t.Errorf("withheld names leaked:\n%s", got)
	}
	// A tight cap drops the count line rather than overflowing.
	tight := FormatCatalog(list, len("# Skills catalog\nNames and one-line descriptions only. Load a body with skill_load when you need the instructions.\n- ok-skill — fine\n"))
	if strings.Contains(tight, "pending review") || !strings.Contains(tight, "- ok-skill — fine") {
		t.Errorf("tight cap handling wrong:\n%s", tight)
	}
	if FormatCatalog([]Skill{{Name: "flagged", NameFlagged: true}}, 10) != "" {
		t.Error("a pending-only catalog that cannot fit its count line must be empty")
	}
}
