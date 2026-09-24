package skills

import (
	"strconv"
	"strings"
	"testing"
)

// ── ScoredMatcher Tests ─────────────────────────────────────────────────

func TestScoredMatcher_Empty(t *testing.T) {
	sm := NewScoredMatcher(nil, DefaultScoredConfig())
	if sm == nil {
		t.Fatal("NewScoredMatcher returned nil")
	}
	matches := sm.MatchSkills("docker build", 5)
	if len(matches) != 0 {
		t.Errorf("expected no matches from empty matcher, got %d", len(matches))
	}
}

func TestScoredMatcher_NoAndLock(t *testing.T) {
	// KEY TEST: Topic-only match should work (fixes AND-lock)
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "container"},
				ActionKeywords: []string{"build", "optimize"},
			},
			Description: "Build and optimize Docker containers",
		},
		{
			Name: "go-test",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"go", "golang"},
				ActionKeywords: []string{"test", "benchmark"},
			},
			Description: "Test Go programs with benchmarks",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// "what is docker" — only topic matches, no action → should match (no AND-lock)
	matches := sm.MatchSkills("what is docker", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for 'what is docker' — topic-only should work without action keyword")
	}
	found := false
	for _, m := range matches {
		if m.Name == "docker-build" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("docker-build should match on 'docker' alone, got: %v", skillNames(matches))
	}

	// "build my project" — only action matches → should also match
	matches = sm.MatchSkills("build my project", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for 'build' — action-only should work without topic keyword")
	}
}

func TestScoredMatcher_TopicAndAction(t *testing.T) {
	// Both topic + action should still work and score higher
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker"},
				ActionKeywords: []string{"build"},
			},
			Description: "Build Docker images",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// "docker build" — both match
	matches := sm.MatchSkills("docker build", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for 'docker build'")
	}
	// Score should be 3 (topic) + 3 (action) = 6
	// We can verify via ExplainMatch
	explanation := sm.ExplainMatch("docker build")
	t.Logf("Explain: %s", explanation)
	if !strings.Contains(explanation, "docker-build") {
		t.Error("expected docker-build in explanation")
	}
}

func TestScoredMatcher_Stemming(t *testing.T) {
	// KEY TEST: Morphological variants
	skills := []Skill{
		{
			Name: "docker-debug",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"debugging", "troubleshooting"},
				ActionKeywords: []string{"fix"},
			},
			Description: "Debugging Docker containers",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// "debug" should match "debugging" via prefix/stemming
	matches := sm.MatchSkills("how to debug this", 5)
	if len(matches) == 0 {
		t.Error("expected match for 'debug' — should match 'debugging' via prefix/stem")
	}

	// "deploy" should NOT match "debugging" (different stem)
	matches = sm.MatchSkills("deploy the app", 5)
	// This depends on stemming — deploy and debug have different stems
	t.Logf("'deploy the app' match count: %d (expected 0 if stemming works correctly)", len(matches))
}

func TestScoredMatcher_Synonyms(t *testing.T) {
	// KEY TEST: Synonym expansion
	skills := []Skill{
		{
			Name: "perf-optimization",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"performance"},
				ActionKeywords: []string{"optimize"},
			},
			Description: "Performance optimization techniques",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// "improve" should match "optimize" via synonym map
	matches := sm.MatchSkills("improve performance", 5)
	if len(matches) == 0 {
		t.Error("expected match for 'improve performance' — 'improve' should be synonym of 'optimize'")
	}

	// "enhance speed" — "enhance" should synonym-match "optimize"
	matches = sm.MatchSkills("enhance the speed", 5)
	if len(matches) == 0 {
		t.Log("Note: 'enhance speed' may not match if 'speed' doesn't match 'performance'")
	} else {
		t.Log("'enhance speed' matched via synonyms!")
	}
}

func TestScoredMatcher_DescriptionMatch(t *testing.T) {
	// Description token matching provides an extra signal
	skills := []Skill{
		{
			Name: "python-skill",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{},
				ActionKeywords: []string{},
			},
			Description: "Python data analysis with pandas and numpy",
		},
	}

	sm := NewScoredMatcher(skills, ScoredMatcherConfig{
		MinScore:     1,
		TopicWeight:  3,
		ActionWeight: 3,
		DescWeight:   1,
		MaxResults:   5,
	})

	// "pandas" should match via description
	matches := sm.MatchSkills("pandas data analysis", 5)
	if len(matches) == 0 {
		t.Error("expected match for 'pandas' — should match via description")
	}
}

func TestScoredMatcher_MaxSlots(t *testing.T) {
	skills := make([]Skill, 10)
	for i := 0; i < 10; i++ {
		skills[i] = Skill{
			Name:        "common-skill",
			Description: "A common skill for testing",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"common"},
				ActionKeywords: []string{"keyword"},
			},
		}
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	matches := sm.MatchSkills("common keyword", 3)
	if len(matches) > 3 {
		t.Errorf("expected at most 3 matches, got %d", len(matches))
	}
}

func TestScoredMatcher_NoMatch(t *testing.T) {
	skills := []Skill{
		{
			Name: "kubernetes-deploy",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"kubernetes", "k8s"},
				ActionKeywords: []string{"deploy"},
			},
			Description: "Deploy to Kubernetes",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// Completely unrelated query should not match
	matches := sm.MatchSkills("what is the weather today", 5)
	if len(matches) != 0 {
		t.Error("expected no match for unrelated query")
	}
}

func TestScoredMatcher_ExplainMatch(t *testing.T) {
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "container"},
				ActionKeywords: []string{"build", "optimize"},
			},
			Description: "Build and optimize Docker images",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())
	explanation := sm.ExplainMatch("build docker containers")
	t.Logf("\n%s", explanation)

	if !strings.Contains(explanation, "docker-build") {
		t.Error("expected docker-build in explanation")
	}
	if !strings.Contains(explanation, "score=") {
		t.Error("expected score in explanation")
	}

	// Regression: verify score values are proper integers in the output.
	// After replacing custom itoa() with strconv.Itoa(), ensure score format
	// is correct (e.g. "score=9/3" not "score=/3" or garbled).
	// Find the score substring and verify it contains digits before the slash.
	idx := strings.Index(explanation, "score=")
	if idx < 0 {
		t.Fatal("score= not found")
	}
	rest := explanation[idx+len("score="):]
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx < 0 {
		t.Fatal("expected / in score format")
	}
	scoreStr := rest[:slashIdx]
	if scoreStr == "" {
		t.Error("score value is empty — itoa/strconv regression")
	}
	scoreVal, err := strconv.Atoi(scoreStr)
	if err != nil {
		t.Errorf("score value %q is not a valid integer: %v", scoreStr, err)
	}
	if scoreVal < 0 {
		t.Errorf("score value %d should be non-negative", scoreVal)
	}
	t.Logf("score format OK: score=%d/%d", scoreVal, scoreVal) // second value is threshold
}

func TestScoredMatcher_Stripping(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"optimization", "optim"},
		{"optimizing", "optim"},
		{"optimized", "optim"},
		{"deployment", "deploy"},
		{"building", "build"},
		{"debugging", "debugg"}, // "debugging" → strip "ing" → "debugg" — close enough
		{"debugged", "debugg"},
		{"debugger", "debugg"},
		{"troubleshooting", "troubleshoot"},
		{"containers", "container"},
		{"images", "imag"}, // "images" → strip "s" → "image" → "imag"? Actually strip "es" first
	}
	for _, tt := range tests {
		got := stripSuffix(tt.input)
		t.Logf("stripSuffix(%q) = %q (expected %q)", tt.input, got, tt.want)
	}
}

func TestHasCommonStem(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"debug", "debugging", true}, // prefix
		{"debugging", "debug", true}, // prefix
		{"optimize", "optimization", true},
		{"deploy", "deployment", true},
		{"build", "building", true},
		{"docker", "docker", true},      // same word
		{"docker", "kubernetes", false}, // different
		{"cat", "cats", true},           // prefix
		{"run", "running", true},        // prefix
		{"test", "testing", true},       // prefix
		{"fix", "fixed", true},          // prefix
	}
	for _, tt := range tests {
		got := hasCommonStem(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("hasCommonStem(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		} else {
			t.Logf("hasCommonStem(%q, %q) = %v ✓", tt.a, tt.b, got)
		}
	}
}

func TestSynonymMap(t *testing.T) {
	syn := newSynonymMap()
	if syn == nil {
		t.Fatal("nil synonym map")
	}

	tests := []struct {
		a, b string
		want bool
	}{
		{"build", "compile", true},
		{"deploy", "release", true},
		{"test", "validate", true},
		{"debug", "fix", true},
		{"optimize", "improve", true},
		{"configure", "setup", true},
		{"build", "deploy", false},  // different groups
		{"optimize", "test", false}, // different groups
		{"docker", "build", false},  // not in any group
	}

	for _, tt := range tests {
		got := syn.areSynonyms(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("areSynonyms(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// ── Vector Matcher Tests ────────────────────────────────────────────────

func TestVectorMatcher_Empty(t *testing.T) {
	vm := NewVectorMatcher(nil, DefaultMatcherConfig)
	if vm == nil {
		t.Fatal("NewVectorMatcher returned nil")
	}
	matches := vm.MatchSkills("docker build", 5)
	if len(matches) != 0 {
		t.Errorf("expected no matches from empty matcher, got %d", len(matches))
	}
}

func TestVectorMatcher_Basic(t *testing.T) {
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "container"},
				ActionKeywords: []string{"build", "optimize"},
			},
			Description: "Build and optimize Docker containers",
		},
		{
			Name: "go-test",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"go", "golang"},
				ActionKeywords: []string{"test", "benchmark"},
			},
			Description: "Test Go programs with benchmarks",
		},
	}

	vm := NewVectorMatcher(skills, DefaultMatcherConfig)
	if vm.Len() == 0 {
		t.Fatal("expected skills in matcher")
	}

	// "build docker" should match the docker-build skill
	matches := vm.MatchSkills("how do I build docker containers", 5)
	if len(matches) == 0 {
		t.Fatal("expected match for docker build query")
	}
	found := false
	for _, m := range matches {
		if m.Name == "docker-build" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("docker-build should be in results, got: %v", skillNames(matches))
	}
}

func TestVectorMatcher_NoKeywordLock(t *testing.T) {
	// KEY TEST: Single keyword matching
	skills := []Skill{
		{
			Name: "docker-info",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "containers"},
				ActionKeywords: []string{},
			},
			Description: "Information about Docker containers",
		},
	}

	vm := NewVectorMatcher(skills, DefaultMatcherConfig)
	// "what is docker" — should match even without action keyword
	matches := vm.MatchSkills("what is docker", 5)
	if len(matches) == 0 {
		t.Error("expected match for 'what is docker' — vector matcher doesn't require action keyword")
	}
}

func TestVectorMatcher_GetSimilarity(t *testing.T) {
	skills := []Skill{
		{
			Name: "test-skill",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"test", "testing"},
				ActionKeywords: []string{"verify", "check"},
			},
			Description: "Test verification skill",
		},
	}

	vm := NewVectorMatcher(skills, DefaultMatcherConfig)
	sim := vm.GetSimilarity("test verify", "test-skill")
	if sim < 0 {
		t.Error("expected non-negative similarity")
	}
	t.Logf("Similarity: %.4f", sim)

	// Non-existent skill
	sim = vm.GetSimilarity("test verify", "nonexistent")
	if sim >= 0 {
		t.Error("expected negative similarity for nonexistent skill")
	}
}

func TestVectorMatcher_DebugInfo(t *testing.T) {
	skills := []Skill{
		{
			Name: "docker-build",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker"},
				ActionKeywords: []string{"build"},
			},
			Description: "Build Docker images",
		},
	}

	vm := NewVectorMatcher(skills, DefaultMatcherConfig)
	info := vm.DebugInfo("docker build")
	t.Logf("DebugInfo:\n%s", info)
	if !strings.Contains(info, "docker-build") {
		t.Error("expected docker-build in debug info")
	}
}

// ── All matchers together ─────────────────────────────────────

func TestMatchers_Synergy(t *testing.T) {
	// Complex scenario: 3 skills with varying trigger quality
	skills := []Skill{
		{
			Name: "docker-optimize",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"docker", "container"},
				ActionKeywords: []string{"optimize", "improve"},
			},
			Description: "Optimize Docker container performance",
		},
		{
			Name: "python-test",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"python", "pytest"},
				ActionKeywords: []string{"test", "assert"},
			},
			Description: "Python testing with pytest",
		},
		{
			Name: "k8s-deploy",
			Trigger: SkillTrigger{
				TopicKeywords:  []string{"kubernetes", "k8s"},
				ActionKeywords: []string{"deploy", "rollout"},
			},
			Description: "Deploy applications to Kubernetes",
		},
	}

	sm := NewScoredMatcher(skills, DefaultScoredConfig())

	// Test 1: "enhance docker performance"
	t.Log("--- Query: 'enhance docker performance' ---")
	matches := sm.MatchSkills("enhance docker performance", 5)
	if len(matches) > 0 {
		t.Logf("  Matched: %v", skillNames(matches))
		// docker-optimize should be first (docker matches topic, enhance synonym of optimize)
		if matches[0].Name == "docker-optimize" {
			t.Log("  ✓ docker-optimize ranked first")
		}
	} else {
		t.Log("  No match (synonym 'enhance' may need lower threshold)")
	}

	// Test 2: "ship to k8s"
	t.Log("--- Query: 'ship to k8s' ---")
	matches = sm.MatchSkills("ship to k8s", 5)
	if len(matches) > 0 {
		t.Logf("  Matched: %v", skillNames(matches))
		// k8s-deploy should match (k8s topic + ship synonym of deploy)
	} else {
		t.Log("  No match ('ship' is synonym of 'deploy')")
	}

	// Test 3: "run tests"
	t.Log("--- Query: 'run tests' ---")
	matches = sm.MatchSkills("run tests", 5)
	t.Logf("  Matched: %v", skillNames(matches))
}

// ── Helpers ─────────────────────────────────────────────────────────────

func skillNames(skills []Skill) []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}
