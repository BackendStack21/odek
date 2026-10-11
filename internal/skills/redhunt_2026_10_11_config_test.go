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

// Short keywords still match their plurals and simple inflections (the old
// prefix rule was their only match, since stripSuffix leaves short words
// alone), while contraction fragments match nothing longer than themselves.
func TestRED_HasCommonStemShortKeywordInflections(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"db", "dbs", true},
		{"dbs", "db", true},
		{"pr", "prs", true},
		{"ci", "cis", true},
		{"ui", "uis", true},
		{"k8", "k8s", true},
		{"go", "going", true},
		{"go", "goes", true},
		{"pr", "prd", false},
		{"go", "golang", false},
		// Contraction fragments.
		{"s", "ss", false},
		{"t", "ts", false},
		{"d", "ds", false},
		{"m", "ms", false},
		{"re", "res", false},
		{"re", "reing", false},
		{"ve", "ves", false},
		{"ll", "lls", false},
		{"s", "security", false},
	} {
		if got := hasCommonStem(tc.a, tc.b); got != tc.want {
			t.Errorf("hasCommonStem(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	// End to end: a short topic keyword matches its plural in a message.
	db := Skill{Name: "db-ops", Description: "Database operations",
		Trigger: SkillTrigger{TopicKeywords: []string{"db"}, ActionKeywords: []string{"migrate"}}, Body: "x"}
	m := NewScoredMatcher([]Skill{db}, DefaultScoredConfig())
	if got := m.MatchSkills("migrate all dbs tonight", 5); len(got) != 1 {
		t.Errorf("\"migrate all dbs tonight\" did not load db-ops (score %d)", m.scoreSkill(db, tokenize("migrate all dbs tonight")))
	}
}
