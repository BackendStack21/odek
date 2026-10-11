package skills

import "testing"

// tokenize turns the apostrophe of an English contraction into a space, so
// "let's" yields the one-letter token "s". A fragment that short must not
// prefix-match every keyword starting with that letter and clear MinScore,
// injecting an unrelated lazy skill's body into the system prompt.
func TestRED_ContractionFragmentDoesNotTriggerSkill(t *testing.T) {
	sec := Skill{
		Name:        "security-audit",
		Description: "Audit dependencies for vulnerabilities",
		Trigger:     SkillTrigger{TopicKeywords: []string{"security"}, ActionKeywords: []string{"scan"}},
		Body:        "Run the full security scan before anything else.",
	}
	rel := Skill{
		Name:        "release-flow",
		Description: "Cut a release",
		Trigger:     SkillTrigger{TopicKeywords: []string{"release"}, ActionKeywords: []string{"refactor"}},
		Body:        "Tag and push.",
	}
	m := NewScoredMatcher([]Skill{sec, rel}, DefaultScoredConfig())
	for _, msg := range []string{"let's rename the parser variable", "we're renaming the parser variable"} {
		if got := m.MatchSkills(msg, 5); len(got) != 0 {
			t.Errorf("message %q loaded %q (score %d) via a contraction fragment",
				msg, got[0].Name, m.scoreSkill(got[0], tokenize(msg)))
		}
	}
	// Real keyword stems still match.
	if got := m.MatchSkills("scan the security of our dependencies", 5); len(got) == 0 || got[0].Name != "security-audit" {
		t.Errorf("security message did not load security-audit: %+v", got)
	}
}

func TestRED_HasCommonStemShortPrefix(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"s", "security", false},
		{"re", "refactor", false},
		{"ve", "verify", false},
		{"s", "s", true}, // exact match still counts
		{"fix", "fixed", true},
		{"securit", "security", true},
	} {
		if got := hasCommonStem(tc.a, tc.b); got != tc.want {
			t.Errorf("hasCommonStem(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
