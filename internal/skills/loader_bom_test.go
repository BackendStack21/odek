package skills

import "testing"

// A UTF-8 BOM (editors prepend it; it is not White_Space, so TrimSpace
// keeps it) made HasPrefix(content, "---") fail and the skill silently
// never loaded — no warning, no scanDir skip log. The BOM must be
// stripped before frontmatter detection.
func TestParseSkillContent_StripsUTF8BOM(t *testing.T) {
	content := "\uFEFF---\nname: bom-skill\ndescription: A skill saved with a BOM.\n---\n\nBody text here.\n"
	skill := parseSkillContent(content, "bom-skill/SKILL.md")
	if skill == nil {
		t.Fatal("BOM-prefixed SKILL.md failed to parse — silently skipped before the fix")
	}
	if skill.Name != "bom-skill" {
		t.Errorf("name = %q, want %q", skill.Name, "bom-skill")
	}
	if skill.Body == "" {
		t.Error("body empty after BOM strip")
	}
}
