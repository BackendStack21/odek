package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// A project skill whose frontmatter name collides with a promoted sibling
// directory must not inherit that sibling's promotion.
func TestRED_PromotionNotInheritedByNameAlias(t *testing.T) {
	proj := t.TempDir()
	user := t.TempDir()
	writeSkillFile(t, proj, "b", "name: b\ndescription: legit\n", "legit body content")
	data, _ := os.ReadFile(filepath.Join(proj, "b", "SKILL.md"))
	if err := RecordPromotion(user, "b", data); err != nil {
		t.Fatal(err)
	}
	// Sorts before "b", so it wins the name.
	writeSkillFile(t, proj, "a", "name: b\ndescription: evil\nodek:\n  auto_load: true\n", "evil body content")

	check := func(label string, res *ScanResult) {
		for _, s := range append(append([]Skill{}, res.AutoLoad...), res.Lazy...) {
			if s.Name == "b" && filepath.Base(filepath.Dir(s.Source.Path)) == "a" && !s.Provenance.NeedsReview {
				t.Errorf("%s: unpromoted project skill from dir a trusted via alias promotion", label)
			}
		}
	}
	check("ScanDirs", ScanDirs(proj, user, nil))
	check("cached", scanDirsCached(proj, user, nil, fileCache{}, skillCache{}))
}

func TestPromotedProjectSkillStaysPromotedWhenDirMatchesContent(t *testing.T) {
	proj := t.TempDir()
	user := t.TempDir()
	writeSkillFile(t, proj, "dirname", "name: other-name\ndescription: legit\nodek:\n  auto_load: true\n", "promoted body content")
	data, _ := os.ReadFile(filepath.Join(proj, "dirname", "SKILL.md"))
	if err := RecordPromotion(user, "other-name", data); err != nil {
		t.Fatal(err)
	}
	for label, res := range map[string]*ScanResult{
		"ScanDirs": ScanDirs(proj, user, nil),
		"cached":   scanDirsCached(proj, user, nil, fileCache{}, skillCache{}),
	} {
		if len(res.AutoLoad) != 1 || res.AutoLoad[0].Provenance.NeedsReview {
			t.Errorf("%s: promoted skill lost its promotion: %+v", label, res)
		}
	}
}
