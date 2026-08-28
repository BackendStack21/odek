package skills

import "testing"

// Regression: parseYAMLValue type-infers scalars, so `version: 1.2`
// became float64(1.2) and `version: 2` became int(2) — both then fell
// through parseSkillContent's bare .(string) assertions and were
// silently dropped (Version = ""). The same drop hit numeric trigger
// topics and provenance sources. String-typed frontmatter fields must
// coerce scalar values (string, int, float64, bool) to their canonical
// string form instead of vanishing.
func TestParseSkillContent_NumericVersionCoerced(t *testing.T) {
	cases := []struct {
		fm   string
		want string
	}{
		{"version: 1.2", "1.2"},
		{"version: 2", "2"},
		{`version: "1.2"`, "1.2"}, // quoted form already worked
		{"version: 1.0", "1"},
	}
	for _, c := range cases {
		content := "---\nname: probe-skill\ndescription: d\n" + c.fm + "\n---\nBody."
		s := parseSkillContent(content, "probe")
		if s == nil {
			t.Fatalf("%s: skill = nil", c.fm)
		}
		if s.Version != c.want {
			t.Errorf("%s: Version = %q, want %q", c.fm, s.Version, c.want)
		}
	}
}

func TestParseSkillContent_NumericAuthorCoerced(t *testing.T) {
	content := "---\nname: probe-skill\ndescription: d\nauthor: 7\n---\nBody."
	s := parseSkillContent(content, "probe")
	if s == nil {
		t.Fatal("skill = nil")
	}
	if s.Author != "7" {
		t.Errorf("author: 7 → Author = %q, want \"7\"", s.Author)
	}
}

func TestParseSkillContent_NumericTriggerTopicCoerced(t *testing.T) {
	content := "---\nname: probe-skill\ndescription: d\nodek:\n  trigger:\n    topic: 2\n---\nBody."
	s := parseSkillContent(content, "probe")
	if s == nil {
		t.Fatal("skill = nil")
	}
	if len(s.Trigger.TopicKeywords) == 0 || s.Trigger.TopicKeywords[0] != "2" {
		t.Errorf("topic: 2 → TopicKeywords = %v, want [\"2\"]", s.Trigger.TopicKeywords)
	}
}

func TestParseSkillContent_BoolAndFloatFormsCoerced(t *testing.T) {
	content := "---\nname: probe-skill\ndescription: 3.14\nodek:\n  auto_load: true\n  trigger:\n    action: 404\n---\nBody."
	s := parseSkillContent(content, "probe")
	if s == nil {
		t.Fatal("skill = nil")
	}
	if s.Description != "3.14" {
		t.Errorf("description: 3.14 → Description = %q, want \"3.14\"", s.Description)
	}
	if len(s.Trigger.ActionKeywords) == 0 || s.Trigger.ActionKeywords[0] != "404" {
		t.Errorf("action: 404 → ActionKeywords = %v, want [\"404\"]", s.Trigger.ActionKeywords)
	}
}
