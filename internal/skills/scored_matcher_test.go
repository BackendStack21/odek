package skills

import (
	"strings"
	"testing"
)

func TestDefaultScoredConfig(t *testing.T) {
	cfg := DefaultScoredConfig()
	if cfg.MinScore != 3 {
		t.Errorf("MinScore = %d, want 3", cfg.MinScore)
	}
	if cfg.TopicWeight != 3 {
		t.Errorf("TopicWeight = %d, want 3", cfg.TopicWeight)
	}
	if cfg.ActionWeight != 3 {
		t.Errorf("ActionWeight = %d, want 3", cfg.ActionWeight)
	}
	if cfg.PrefixWeight != 2 {
		t.Errorf("PrefixWeight = %d, want 2", cfg.PrefixWeight)
	}
	if cfg.DescWeight != 1 {
		t.Errorf("DescWeight = %d, want 1", cfg.DescWeight)
	}
	if cfg.SynonymWeight != 2 {
		t.Errorf("SynonymWeight = %d, want 2", cfg.SynonymWeight)
	}
	if cfg.MaxResults != 5 {
		t.Errorf("MaxResults = %d, want 5", cfg.MaxResults)
	}
	if !cfg.EnableSynonyms {
		t.Error("EnableSynonyms should be true")
	}
	if !cfg.EnableStemming {
		t.Error("EnableStemming should be true")
	}
}

func TestNewScoredMatcher_ZeroConfigUsesDefaults(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, ScoredMatcherConfig{})
	if sm == nil {
		t.Fatal("NewScoredMatcher returned nil")
	}
	if sm.cfg.MinScore != 3 {
		t.Errorf("MinScore should default to 3, got %d", sm.cfg.MinScore)
	}
	if sm.cfg.TopicWeight != 3 {
		t.Errorf("TopicWeight should default to 3, got %d", sm.cfg.TopicWeight)
	}
	if sm.cfg.MaxResults != 5 {
		t.Errorf("MaxResults should default to 5, got %d", sm.cfg.MaxResults)
	}
}

func TestNewScoredMatcher_CustomConfigPreserved(t *testing.T) {
	cfg := ScoredMatcherConfig{
		MinScore:     5,
		TopicWeight:  2,
		ActionWeight: 2,
		MaxResults:   10,
	}
	sm := NewScoredMatcher(nil, cfg)
	if sm.cfg.MinScore != 5 {
		t.Errorf("MinScore = %d, want 5", sm.cfg.MinScore)
	}
	if sm.cfg.TopicWeight != 2 {
		t.Errorf("TopicWeight = %d, want 2", sm.cfg.TopicWeight)
	}
	if sm.cfg.MaxResults != 10 {
		t.Errorf("MaxResults = %d, want 10", sm.cfg.MaxResults)
	}
}

func TestMatchSkills_NilReceiver(t *testing.T) {
	var sm *ScoredMatcher
	result := sm.MatchSkills("test", 5)
	if result != nil {
		t.Error("expected nil for nil receiver")
	}
}

func TestMatchSkills_EmptySkills(t *testing.T) {
	sm := NewScoredMatcher([]Skill{}, DefaultScoredConfig())
	result := sm.MatchSkills("test", 5)
	if result != nil {
		t.Error("expected nil for empty skills")
	}
}

func TestMatchSkills_EmptyInput(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("", 5)
	if result != nil {
		t.Error("expected nil for empty input")
	}
}

func TestMatchSkills_ZeroMaxSlots(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("docker", 0)
	if result != nil {
		t.Error("expected nil for zero maxSlots")
	}
}

func TestMatchSkills_ExactTopicMatch(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
		{Name: "go-test", Trigger: SkillTrigger{TopicKeywords: []string{"golang"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("docker build image", 5)
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Name != "docker-build" {
		t.Errorf("expected 'docker-build', got %q", result[0].Name)
	}
}

func TestMatchSkills_ActionMatch(t *testing.T) {
	skills := []Skill{
		{Name: "install-pkg", Trigger: SkillTrigger{
			TopicKeywords:  []string{"package"},
			ActionKeywords: []string{"install"},
		}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("install the package", 5)
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Name != "install-pkg" {
		t.Errorf("expected 'install-pkg', got %q", result[0].Name)
	}
}

func TestMatchSkills_DescriptionMatch(t *testing.T) {
	skills := []Skill{
		{Name: "test-skill", Description: "guide for testing code with coverage", Trigger: SkillTrigger{}},
	}
	sm := NewScoredMatcher(skills, ScoredMatcherConfig{
		MinScore:   1,
		DescWeight: 1,
	})
	result := sm.MatchSkills("code coverage testing", 5)
	if len(result) == 0 {
		t.Fatal("expected at least 1 match from description tokens")
	}
}

func TestMatchSkills_ScoreSorting(t *testing.T) {
	skills := []Skill{
		{Name: "a-skill", Trigger: SkillTrigger{TopicKeywords: []string{"alpha"}}},
		{Name: "b-skill", Trigger: SkillTrigger{
			TopicKeywords:  []string{"beta"},
			ActionKeywords: []string{"build"},
		}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("beta build alpha", 5)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(result))
	}
	if result[0].Name != "b-skill" {
		t.Errorf("expected b-skill first (higher score), got %q", result[0].Name)
	}
}

func TestMatchSkills_MaxSlotsCapsResults(t *testing.T) {
	skills := []Skill{
		{Name: "skill-a", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
		{Name: "skill-b", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
		{Name: "skill-c", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("docker", 2)
	if len(result) > 2 {
		t.Errorf("expected max 2 results, got %d", len(result))
	}
}

func TestMatchSkills_NoMatch(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("completely unrelated topic", 5)
	if result != nil {
		t.Error("expected nil for no match")
	}
}

func TestScoreSkill_TopicKeywords(t *testing.T) {
	skill := Skill{
		Name: "docker-build",
		Trigger: SkillTrigger{
			TopicKeywords: []string{"docker", "container"},
		},
	}
	sm := NewScoredMatcher(nil, DefaultScoredConfig())
	tokens := tokenize("docker")
	score := sm.scoreSkill(skill, tokens)
	if score < 3 {
		t.Errorf("expected score >= 3 for docker topic match, got %d", score)
	}
}

func TestScoreSkill_ActionKeywords(t *testing.T) {
	skill := Skill{
		Name: "install-pkg",
		Trigger: SkillTrigger{
			TopicKeywords:  []string{"package"},
			ActionKeywords: []string{"install"},
		},
	}
	sm := NewScoredMatcher(nil, DefaultScoredConfig())
	tokens := tokenize("install")
	score := sm.scoreSkill(skill, tokens)
	if score < 3 {
		t.Errorf("expected score >= 3 for action match, got %d", score)
	}
}

func TestScoreSkill_NoMatch(t *testing.T) {
	skill := Skill{
		Name: "docker-build",
		Trigger: SkillTrigger{
			TopicKeywords: []string{"docker"},
		},
	}
	sm := NewScoredMatcher(nil, DefaultScoredConfig())
	tokens := tokenize("golang")
	score := sm.scoreSkill(skill, tokens)
	if score != 0 {
		t.Errorf("expected 0 for no match, got %d", score)
	}
}

func TestMatchSkills_DeterministicOrder(t *testing.T) {
	skills := []Skill{
		{Name: "z-skill", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
		{Name: "a-skill", Trigger: SkillTrigger{TopicKeywords: []string{"docker"}}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	result := sm.MatchSkills("docker", 5)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	if result[0].Name != "a-skill" {
		t.Errorf("expected 'a-skill' first (alphabetical), got %q", result[0].Name)
	}
	if result[1].Name != "z-skill" {
		t.Errorf("expected 'z-skill' second, got %q", result[1].Name)
	}
}

func TestExplainMatch_Format(t *testing.T) {
	skills := []Skill{
		{Name: "docker-build", Description: "Build Docker images", Trigger: SkillTrigger{
			TopicKeywords:  []string{"docker"},
			ActionKeywords: []string{"build"},
		}},
	}
	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	output := sm.ExplainMatch("build docker images")
	if output == "" {
		t.Fatal("ExplainMatch returned empty string")
	}
	if !strings.Contains(output, "docker-build") {
		t.Errorf("expected output to contain 'docker-build', got: %s", output)
	}
	if !strings.Contains(output, "3") {
		t.Errorf("expected output to contain score, got: %s", output)
	}
}

func TestPrefixWeight_UsesDefault(t *testing.T) {
	cfg := ScoredMatcherConfig{MinScore: 1}
	sm := NewScoredMatcher(nil, cfg)
	if sm.cfg.PrefixWeight != 2 {
		t.Errorf("PrefixWeight should default to 2, got %d", sm.cfg.PrefixWeight)
	}
}
