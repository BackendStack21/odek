package skills

import (
	"strings"
	"testing"
)

func TestSameSkillContent(t *testing.T) {
	base := "---\nname: rb\ndescription: d\nversion: \"1\"\nodek:\n  auto_load: true\n  trigger:\n    topic: go\n---\nbody text\n"
	s := parseSkillContent(base, "/x/rb/SKILL.md")
	if s == nil {
		t.Fatal("fixture did not parse")
	}
	s.Source.Path = "/x/rb/SKILL.md"
	if !SameSkillContent([]byte(base), *s) {
		t.Fatal("identical bytes must match")
	}
	if !SameSkillContent([]byte("\uFEFF"+base+"\n\n"), *s) {
		t.Fatal("BOM and trailing whitespace do not change the skill")
	}
	for name, mut := range map[string]string{
		"body":        strings.Replace(base, "body text", "other", 1),
		"description": strings.Replace(base, "description: d", "description: e", 1),
		"version":     strings.Replace(base, "version: \"1\"", "version: \"2\"", 1),
		"auto_load":   strings.Replace(base, "auto_load: true", "auto_load: false", 1),
		"trigger":     strings.Replace(base, "topic: go", "topic: rust", 1),
		"name":        strings.Replace(base, "name: rb", "name: rc", 1),
		"provenance":  strings.Replace(base, "odek:\n", "odek:\n  provenance:\n    untrusted: true\n", 1),
		"unparseable": "not a skill",
	} {
		if SameSkillContent([]byte(mut), *s) {
			t.Errorf("%s change accepted", name)
		}
	}
	if SameSkillContent([]byte(base+strings.Repeat("x", MaxSkillFileBytes)), *s) {
		t.Error("oversized content accepted")
	}

	// A project-dir scan marks the skill for review; the unchanged bytes
	// still match the marked skill.
	marked := *s
	markProjectSkill(&marked)
	if !SameSkillContent([]byte(base), marked) {
		t.Error("project-marked skill must match its unchanged file")
	}
}

func TestIsPromotedContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("---\nname: pc\ndescription: d\n---\nb\n")
	if IsPromotedContent(dir, "pc", content) {
		t.Fatal("unrecorded content reported promoted")
	}
	if err := RecordPromotion(dir, "pc", content); err != nil {
		t.Fatal(err)
	}
	if !IsPromotedContent(dir, "pc", content) {
		t.Fatal("recorded content not reported promoted")
	}
	if IsPromotedContent(dir, "pc", append(content, '!')) {
		t.Fatal("changed content reported promoted")
	}
	if IsPromotedContent("", "pc", content) {
		t.Fatal("empty registry dir reported promoted")
	}
}
