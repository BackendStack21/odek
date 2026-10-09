package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeShadowSkill(t *testing.T, root, dirName, name, desc, extraOdek string) {
	t.Helper()
	p := filepath.Join(root, dirName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n%s---\n\n%s", name, desc, extraOdek, catalogTestBody())
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findSkill(res *ScanResult, name string) []Skill {
	var out []Skill
	for _, list := range [][]Skill{res.AutoLoad, res.Lazy} {
		for _, s := range list {
			if s.Name == name {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestRED_ScanDirs_TrustedSkillWinsOverProjectShadow(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeShadowSkill(t, user, "deploy", "deploy", "trusted user skill", "")
	writeShadowSkill(t, project, "deploy", "deploy", "repo shadow", "")

	for name, res := range map[string]*ScanResult{
		"ScanDirs":       ScanDirs(project, user, nil),
		"scanDirsCached": scanDirsCached(project, user, nil, make(fileCache), make(skillCache)),
	} {
		got := findSkill(res, "deploy")
		if len(got) != 1 {
			t.Fatalf("%s: want exactly one deploy skill, got %d", name, len(got))
		}
		if got[0].Description != "trusted user skill" || got[0].Provenance.NeedsReview {
			t.Errorf("%s: project skill shadowed the trusted one: %+v", name, got[0])
		}
	}
}

func TestRED_ScanDirs_TrustedExtraWinsOverNeedsReviewUserSkill(t *testing.T) {
	user := t.TempDir()
	extra := t.TempDir()
	writeShadowSkill(t, user, "fmt", "fmt", "imported pending review", "odek:\n  provenance:\n    untrusted: true\n    needs_review: true\n")
	writeShadowSkill(t, extra, "fmt", "fmt", "trusted extra", "")

	res := ScanDirs("", user, []string{extra})
	got := findSkill(res, "fmt")
	if len(got) != 1 || got[0].Description != "trusted extra" {
		t.Fatalf("NeedsReview skill shadowed a trusted extra-dir skill: %+v", got)
	}
}

func TestScanDirs_ProjectOnlySkillStillLoads(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeShadowSkill(t, project, "only-here", "only-here", "repo skill", "")
	got := findSkill(ScanDirs(project, user, nil), "only-here")
	if len(got) != 1 || !got[0].Provenance.NeedsReview {
		t.Fatalf("project-only skill must load pinned NeedsReview: %+v", got)
	}
}
