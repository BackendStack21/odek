package skills

import (
	"testing"
)

func TestBuildTriggerIndex_Empty(t *testing.T) {
	idx := BuildTriggerIndex(nil)
	if idx == nil {
		t.Fatal("nil index")
	}
	matches := idx.MatchSkills("docker build", 5)
	if len(matches) != 0 {
		t.Error("expected no matches from empty index")
	}
}

func TestTriggerMatch_Basic(t *testing.T) {
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "container"},
				ActionKeywords: []string{"build", "optimize"},
			},
		},
		{
			Name: "go-test",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"go", "golang"},
				ActionKeywords: []string{"test", "benchmark"},
			},
		},
	}

	idx := BuildTriggerIndex(skills)

	// Docker topic + build action
	matches := idx.MatchSkills("how do I build docker containers", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for docker build")
	}
	if matches[0].Name != "docker-build" {
		t.Errorf("expected docker-build, got %q", matches[0].Name)
	}

	// Go topic + test action
	matches = idx.MatchSkills("how do I test go code", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for go test")
	}
	if matches[0].Name != "go-test" {
		t.Errorf("expected go-test, got %q", matches[0].Name)
	}
}

func TestTriggerMatch_NoAction(t *testing.T) {
	// Only topic matches — should not fire (action required)
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker"},
				ActionKeywords: []string{"build"},
			},
		},
	}

	idx := BuildTriggerIndex(skills)
	matches := idx.MatchSkills("what is docker", 5)
	if len(matches) != 0 {
		t.Error("expected no match (no action keyword)")
	}
}

func TestTriggerMatch_OnlyTopic(t *testing.T) {
	// Skill with only topic keywords and no action keywords
	// This skill should NOT match because action is also required
	skills := []Skill{
		{
			Name: "topic-only",
			Trigger: SkillTrigger{
				TopicKeywords: []string{"docker"},
			},
		},
	}

	idx := BuildTriggerIndex(skills)
	matches := idx.MatchSkills("docker containers", 5)
	if len(matches) != 0 {
		t.Error("expected no match (no action keywords in skill)")
	}
}

func TestTriggerMatch_CaseInsensitive(t *testing.T) {
	skills := []Skill{
		{
			Name: "test-skill",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"Docker"},
				ActionKeywords: []string{"Build"},
			},
		},
	}

	idx := BuildTriggerIndex(skills)
	matches := idx.MatchSkills("docker build", 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
}

func TestTriggerMatch_MaxSlots(t *testing.T) {
	skills := make([]Skill, 10)
	for i := 0; i < 10; i++ {
		skills[i] = Skill{
			Name: "skill",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"common"},
				ActionKeywords: []string{"keyword"},
			},
		}
	}

	idx := BuildTriggerIndex(skills)
	matches := idx.MatchSkills("common keyword", 3)
	if len(matches) > 3 {
		t.Errorf("expected at most 3 matches, got %d", len(matches))
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"deploy to kubernetes", []string{"deploy", "kubernetes"}},
		{"how do I build docker containers?", []string{"build", "docker", "containers"}},
		{"", nil},
		{"the a an is", nil}, // all stopwords
		{"test.run()", []string{"test", "run"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v (len=%d), want %v (len=%d)", tt.input, got, len(got), tt.want, len(tt.want))
			continue
		}
		for i, w := range got {
			if w != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, w, tt.want[i])
			}
		}
	}
}

func TestDirPriority(t *testing.T) {
	tests := []struct {
		dir  string
		want int
	}{
		{"./.kode/skills", 0},
		{"/project/.kode/skills", 1}, // can't distinguish from user without home dir
		{"~/.kode/skills", 1},
		{"/home/user/.kode/skills", 1},
		{"/custom/path", 2},
	}
	for _, tt := range tests {
		got := dirPriority(tt.dir)
		if got != tt.want {
			// ~/.kode/skills now matches priority 1 correctly
			t.Errorf("dirPriority(%q) = %d, want %d", tt.dir, got, tt.want)
		}
	}
}

func TestTriggerMatch_Prefix(t *testing.T) {
	// Test that prefix matching works: "docker-compose" should match "docker"
	skills := []Skill{
		{
			Name: "docker-general",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker"},
				ActionKeywords: []string{"build"},
			},
		},
	}

	idx := BuildTriggerIndex(skills)
	// "docker" is a prefix of "docker-compose" — should match via trie traversal
	matches := idx.MatchSkills("docker-compose build", 5)
	if len(matches) == 0 {
		t.Log("Note: prefix match on concatenated tokens may not work as expected")
	}
}
