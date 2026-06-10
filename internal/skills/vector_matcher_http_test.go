package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/embedding"
)

// mockSkillEmbedServer serves the OpenAI embeddings wire format with
// keyword-bucketed vectors so semantically related skill texts and queries get
// aligned vectors even without lexical overlap.
func mockSkillEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i, txt := range req.Input {
			out.Data = append(out.Data, datum{Index: i, Embedding: skillVectorFor(txt)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func skillVectorFor(text string) []float32 {
	v := make([]float32, 8)
	buckets := map[int][]string{
		0: {"docker", "container", "containers", "image", "kubernetes"},
		1: {"go", "golang", "test", "benchmark"},
	}
	words := strings.Fields(strings.ToLower(text))
	for dim, bucket := range buckets {
		for _, kw := range bucket {
			for _, tok := range words {
				if strings.Trim(tok, ".,;:!?[]") == kw {
					v[dim] = 1
				}
			}
		}
	}
	v[7] = 0.1
	return v
}

func dockerAndGoSkills() []Skill {
	return []Skill{
		{
			Name:        "docker-build",
			Trigger:     SkillTrigger{TopicKeywords: []string{"docker", "container"}, ActionKeywords: []string{"build"}},
			Description: "Build and optimize Docker containers",
		},
		{
			Name:        "go-test",
			Trigger:     SkillTrigger{TopicKeywords: []string{"go", "golang"}, ActionKeywords: []string{"test"}},
			Description: "Test Go programs with benchmarks",
		},
	}
}

func httpSkillsConfig(srv *httptest.Server) *embedding.Config {
	return &embedding.Config{Provider: "http", BaseURL: srv.URL + "/v1", Model: "mock-embed"}
}

// TestVectorMatcherHTTPSemantic: an HTTP-backed matcher reports Semantic() and
// matches by meaning — "kubernetes image" hits the docker skill though it
// shares no keyword with it.
func TestVectorMatcherHTTPSemantic(t *testing.T) {
	srv := mockSkillEmbedServer(t)
	vm := NewVectorMatcherWithConfig(dockerAndGoSkills(), DefaultMatcherConfig, httpSkillsConfig(srv))

	if !vm.Semantic() {
		t.Fatal("Semantic() should be true for an HTTP backend")
	}
	matches := vm.MatchSkills("kubernetes image registry", 5)
	if len(matches) == 0 || matches[0].Name != "docker-build" {
		t.Fatalf("matches = %v, want docker-build first (semantic match)", skillNames(matches))
	}
}

// TestVectorMatcherDefaultNotSemantic: the default RP backend is local, so
// Semantic() is false and the manager keeps the keyword matcher as primary.
func TestVectorMatcherDefaultNotSemantic(t *testing.T) {
	vm := NewVectorMatcher(dockerAndGoSkills(), DefaultMatcherConfig)
	if vm.Semantic() {
		t.Error("default RP matcher must not report Semantic()")
	}
}

// TestMatchLazySkillsFallsBackOnDownBackend: when the opt-in HTTP backend is
// down, the manager's MatchLazySkills degrades to the keyword ScoredMatcher
// rather than returning nothing.
func TestMatchLazySkillsFallsBackOnDownBackend(t *testing.T) {
	// A server that always errors simulates a down embedding backend.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	sm := NewSkillManagerWithEmbedding(dir, dir, httpSkillsConfig(srv))
	// Inject skills directly (no files on disk) and rebuild the matchers.
	sm.Result.Lazy = dockerAndGoSkills()
	sm.ScoredMatcher = NewScoredMatcher(sm.Result.Lazy, DefaultScoredConfig())
	sm.VectorMatcher = NewVectorMatcherWithConfig(sm.Result.Lazy, DefaultMatcherConfig, httpSkillsConfig(srv))

	// The vector matcher is "semantic" but its store is empty (corpus embed
	// failed) and the query embed errors — MatchLazySkills must fall back to the
	// keyword ScoredMatcher, which matches on the literal keyword.
	matches := sm.MatchLazySkills("docker build", 5)
	if len(matches) == 0 {
		t.Fatal("expected keyword fallback to match 'docker build' when backend is down")
	}
	found := false
	for _, m := range matches {
		if m.Name == "docker-build" {
			found = true
		}
	}
	if !found {
		t.Errorf("fallback matches = %v, want docker-build", skillNames(matches))
	}
}

// TestMatchLazySkillsSemanticSuccess: with a healthy HTTP backend, the manager
// returns the semantic match directly (the semantic-first branch), not the
// keyword fallback.
func TestMatchLazySkillsSemanticSuccess(t *testing.T) {
	srv := mockSkillEmbedServer(t)
	dir := t.TempDir()
	sm := NewSkillManagerWithEmbedding(dir, dir, httpSkillsConfig(srv))
	sm.Result.Lazy = dockerAndGoSkills()
	sm.ScoredMatcher = NewScoredMatcher(sm.Result.Lazy, DefaultScoredConfig())
	sm.VectorMatcher = NewVectorMatcherWithConfig(sm.Result.Lazy, DefaultMatcherConfig, httpSkillsConfig(srv))

	// "kubernetes" shares no keyword with the docker skill, so a hit here proves
	// the semantic path ran (the keyword ScoredMatcher would miss it).
	matches := sm.MatchLazySkills("kubernetes image registry", 5)
	if len(matches) == 0 || matches[0].Name != "docker-build" {
		t.Fatalf("MatchLazySkills = %v, want docker-build via semantic match", skillNames(matches))
	}
}

// TestHybridMatcher exercises the trie+vector merge: trie hits come first,
// vector fills the remaining slots without duplicating.
func TestHybridMatcher(t *testing.T) {
	hm := NewHybridMatcher(dockerAndGoSkills(), DefaultMatcherConfig)
	matches := hm.MatchSkills("docker build", 5)
	if len(matches) == 0 {
		t.Fatal("hybrid matcher returned no matches for 'docker build'")
	}
	// No duplicate skill names in the merged result.
	seen := map[string]bool{}
	for _, m := range matches {
		if seen[m.Name] {
			t.Errorf("duplicate skill %q in hybrid result", m.Name)
		}
		seen[m.Name] = true
	}
	if !seen["docker-build"] {
		t.Errorf("expected docker-build in hybrid result, got %v", skillNames(matches))
	}
}

// TestBuildEmbedTextVariants covers the non-merged topic/action layout and the
// include-body branch of buildEmbedText.
func TestBuildEmbedTextVariants(t *testing.T) {
	s := Skill{
		Name:        "x",
		Trigger:     SkillTrigger{TopicKeywords: []string{"docker"}, ActionKeywords: []string{"build"}},
		Description: "Build images",
		Body:        "long body text about layers",
	}
	separate := buildEmbedText(s, MatcherConfig{MergeTopicAction: false, IncludeBody: true})
	for _, want := range []string{"docker", "build", "Build images", "long body text"} {
		if !strings.Contains(separate, want) {
			t.Errorf("buildEmbedText(separate+body) missing %q in %q", want, separate)
		}
	}
	merged := buildEmbedText(s, MatcherConfig{MergeTopicAction: true, IncludeBody: false})
	if strings.Contains(merged, "long body text") {
		t.Errorf("buildEmbedText should omit body when IncludeBody=false: %q", merged)
	}
}

// TestMatchLazySkillsFallbackChain covers the non-semantic fallback order:
// vector matcher when the scored matcher is absent, then trie when both are.
func TestMatchLazySkillsFallbackChain(t *testing.T) {
	dir := t.TempDir()
	sm := NewSkillManager(dir, dir) // local RP, not semantic
	sm.Result.Lazy = dockerAndGoSkills()
	sm.ScoredMatcher = nil
	sm.VectorMatcher = NewVectorMatcher(sm.Result.Lazy, DefaultMatcherConfig)
	sm.TrieIndex = BuildTriggerIndex(sm.Result.Lazy)

	// scored == nil → vector matcher path.
	if m := sm.MatchLazySkills("docker build container", 5); len(m) == 0 {
		t.Error("expected vector-matcher fallback to match when scored matcher is nil")
	}

	// scored and vector nil → trie path.
	sm.VectorMatcher = nil
	if m := sm.MatchLazySkills("docker build", 5); len(m) == 0 {
		t.Error("expected trie fallback to match when scored and vector matchers are nil")
	}

	// Everything nil → no panic, empty result.
	sm.TrieIndex = nil
	if m := sm.MatchLazySkills("docker build", 5); len(m) != 0 {
		t.Errorf("all matchers nil should yield no matches, got %v", skillNames(m))
	}
}
