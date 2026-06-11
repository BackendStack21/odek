// Package skills — vector-based skill matching using go-vector.
//
// Replaces the brittle keyword trie with RandomProjections embedding + Store
// for semantic nearest-neighbor search. Solves:
//   - AND-lock: no separate topic/action requirement, single query embedding
//   - Morphological variants: "debugging" ≈ "debug", "optimization" ≈ "optimize"
//   - Partial semantic similarity: "improve performance" ≈ "optimize speed"
//   - Graceful degradation: always returns top-k with configurable threshold
package skills

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/BackendStack21/go-vector/pkg/vector"
	"github.com/BackendStack21/odek/internal/embedding"
)

// skillsQueryTimeoutSeconds bounds the per-turn query embed for a remote
// (HTTP) skills embedder. Skill matching runs on every user turn, so a slow or
// down backend must not stall the loop — it times out fast and the caller
// falls back to the keyword ScoredMatcher. Only applied when the opt-in HTTP
// embedding config leaves timeout_seconds unset.
const skillsQueryTimeoutSeconds = 2

// DefaultMatcherConfig provides sensible defaults for the vector matcher.
var DefaultMatcherConfig = MatcherConfig{
	OutputDim:        256,   // 256-dim RP is good for semantic similarity
	MinSimilarity:    0.35,  // minimum cosine similarity to consider a match
	MaxResults:       5,     // max skills to load per query
	MergeTopicAction: true,  // embed topic+action combined for better signals
	IncludeBody:      false, // optionally include body for richer embeddings (slower)
}

// MatcherConfig controls the vector-based skill matcher.
type MatcherConfig struct {
	OutputDim        int     `json:"output_dim"`         // RP output dimensionality
	MinSimilarity    float32 `json:"min_similarity"`     // cosine threshold [0,1]
	MaxResults       int     `json:"max_results"`        // max skills returned
	MergeTopicAction bool    `json:"merge_topic_action"` // combine topic+action into one embedding
	IncludeBody      bool    `json:"include_body"`       // include body text in embedding
}

// VectorMatcher matches skills against user input via cosine similarity over
// the shared embedding backend (internal/embedding): RandomProjections by
// default, or an opt-in OpenAI-compatible HTTP embeddings API for real semantic
// matching. The skill corpus embeds once at build (cheap); only the query
// embeds per turn — so the HTTP backend is opt-in and time-bounded.
type VectorMatcher struct {
	store    *vector.Store
	emb      embedding.TextEmbedder
	skills   []Skill  // parallel to store IDs
	ids      []string // store IDs = skill names
	cfg      MatcherConfig
	semantic bool // true when emb is a remote (HTTP) backend
}

// NewVectorMatcher builds a vector matcher over the default RandomProjections
// backend. Equivalent to NewVectorMatcherWithConfig(skills, cfg, nil).
func NewVectorMatcher(skills []Skill, cfg MatcherConfig) *VectorMatcher {
	return NewVectorMatcherWithConfig(skills, cfg, nil)
}

// NewVectorMatcherWithConfig builds a vector matcher using the embedding
// backend selected by embCfg (nil = default RandomProjections). When embCfg
// selects the HTTP provider, a short query timeout is applied unless the config
// sets one, since the query embeds on the per-turn hot path.
func NewVectorMatcherWithConfig(skills []Skill, cfg MatcherConfig, embCfg *embedding.Config) *VectorMatcher {
	if cfg.OutputDim <= 0 {
		cfg.OutputDim = DefaultMatcherConfig.OutputDim
	}
	if cfg.MinSimilarity <= 0 {
		cfg.MinSimilarity = DefaultMatcherConfig.MinSimilarity
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = DefaultMatcherConfig.MaxResults
	}

	embCfg = withSkillsQueryTimeout(embCfg)
	emb := embedding.New(embCfg, cfg.OutputDim)

	vm := &VectorMatcher{
		store:    vector.NewStore(vector.CosineDistance),
		emb:      emb,
		skills:   make([]Skill, 0, len(skills)),
		ids:      make([]string, 0, len(skills)),
		cfg:      cfg,
		semantic: strings.HasPrefix(emb.Fingerprint(), "http/"),
	}

	// Build corpus texts and fit the embedder vocabulary / warm its cache.
	corpus := make([]string, 0, len(skills))
	for _, s := range skills {
		corpus = append(corpus, buildEmbedText(s, cfg))
	}
	if err := vm.emb.Fit(corpus); err != nil {
		// A down backend at build time leaves an empty matcher; callers fall
		// back to the keyword matcher.
		return vm
	}

	// Batch-embed the corpus (one HTTP call for the remote backend).
	vecs, err := vm.emb.EmbedAll(corpus)
	if err != nil {
		return vm
	}
	for i, s := range skills {
		if vecs[i] == nil {
			continue // skip skills that fail to embed
		}
		vm.store.Add(s.Name, vecs[i])
		vm.skills = append(vm.skills, s)
		vm.ids = append(vm.ids, s.Name)
	}

	return vm
}

// Semantic reports whether the matcher uses a remote (HTTP) embedding backend.
// Callers use this to prefer semantic matching only when it is configured,
// keeping the per-turn keyword path the default.
func (vm *VectorMatcher) Semantic() bool {
	return vm != nil && vm.semantic
}

// withSkillsQueryTimeout returns embCfg with a short default query timeout for
// the HTTP provider, so the per-turn query embed fails fast instead of stalling
// the loop. A timeout the user set explicitly is left untouched. RP configs are
// returned unchanged.
func withSkillsQueryTimeout(embCfg *embedding.Config) *embedding.Config {
	if embCfg == nil || embCfg.Provider != "http" || embCfg.TimeoutSeconds > 0 {
		return embCfg
	}
	clone := *embCfg
	clone.TimeoutSeconds = skillsQueryTimeoutSeconds
	return &clone
}

// MatchSkills returns skills matching the user input, ranked by cosine similarity.
// Returns at most cfg.MaxResults skills with similarity >= cfg.MinSimilarity.
// This is a drop-in replacement for triggerIndex.MatchSkills.
func (vm *VectorMatcher) MatchSkills(userInput string, maxSlots int) []Skill {
	if vm == nil || vm.store == nil || vm.store.Len() == 0 || maxSlots <= 0 {
		return nil
	}

	// Apply maxSlots (from config) but also cap at cfg.MaxResults
	k := maxSlots
	if vm.cfg.MaxResults > 0 && k > vm.cfg.MaxResults {
		k = vm.cfg.MaxResults
	}
	if k > vm.store.Len() {
		k = vm.store.Len()
	}

	// Embed the user query
	queryVec, err := vm.emb.Embed(userInput)
	if err != nil || queryVec == nil {
		return nil
	}

	// Search the store
	results := vm.store.Search(queryVec, k)
	if len(results) == 0 {
		return nil
	}

	// Convert cosine distance (1 - similarity) back to similarity,
	// filter by threshold, and build result slice
	type scored struct {
		skill      Skill
		similarity float32
	}

	var scoredSkills []scored
	for _, r := range results {
		// Store uses CosineDistance = 1 - CosineSimilarity
		// So similarity = 1 - distance
		similarity := 1 - r.Distance
		if similarity < vm.cfg.MinSimilarity {
			continue
		}
		if math.IsNaN(float64(similarity)) {
			continue
		}

		// Find the skill by ID
		for _, s := range vm.skills {
			if s.Name == r.ID {
				scoredSkills = append(scoredSkills, scored{skill: s, similarity: similarity})
				break
			}
		}
	}

	if len(scoredSkills) == 0 {
		return nil
	}

	// Sort by similarity descending
	sort.Slice(scoredSkills, func(i, j int) bool {
		return scoredSkills[i].similarity > scoredSkills[j].similarity
	})

	// Cap at maxSlots
	if len(scoredSkills) > maxSlots {
		scoredSkills = scoredSkills[:maxSlots]
	}

	// Extract skills
	matched := make([]Skill, len(scoredSkills))
	for i, s := range scoredSkills {
		matched[i] = s.skill
	}

	return matched
}

// GetSimilarity returns the cosine similarity between a user query and a skill
// by name. Returns -1 if the skill is not found or embedding fails.
func (vm *VectorMatcher) GetSimilarity(userInput, skillName string) float32 {
	if vm == nil || vm.store == nil {
		return -1
	}

	queryVec, err := vm.emb.Embed(userInput)
	if err != nil || queryVec == nil {
		return -1
	}

	skillVec := vm.store.Get(skillName)
	if skillVec == nil {
		return -1
	}

	return 1 - vector.CosineDist(queryVec, skillVec)
}

// Len returns the number of skills in the matcher.
func (vm *VectorMatcher) Len() int {
	if vm == nil || vm.store == nil {
		return 0
	}
	return vm.store.Len()
}

// ── Helpers ─────────────────────────────────────────────────────────────

// buildEmbedText constructs the text to embed for a skill.
// Combines topic + action keywords (and optionally body) into one string
// so the RP captures the full semantic profile in a single vector.
func buildEmbedText(s Skill, cfg MatcherConfig) string {
	var text string

	if cfg.MergeTopicAction {
		// Combine all keywords into one bag-of-words
		allKWs := append(s.Trigger.TopicKeywords, s.Trigger.ActionKeywords...)
		for i, kw := range allKWs {
			if i > 0 {
				text += " "
			}
			text += kw
		}
		text += " "
	} else {
		// Keep topic and action as separate sections
		for i, kw := range s.Trigger.TopicKeywords {
			if i > 0 {
				text += " "
			}
			text += kw
		}
		text += " "
		for i, kw := range s.Trigger.ActionKeywords {
			if i > 0 {
				text += " "
			}
			text += kw
		}
		text += " "
	}

	// Add description for more semantic signal
	if s.Description != "" {
		text += s.Description + " "
	}

	// Optionally add body (more signal but larger vocab = more memory)
	if cfg.IncludeBody {
		text += s.Body
	}

	return text
}

// ── Hybrid Mode ─────────────────────────────────────────────────────────

// HybridMatcher combines keyword trie (high precision) with vector search
// (high recall) for the best of both worlds.
type HybridMatcher struct {
	trie   *triggerIndex
	vector *VectorMatcher
	cfg    MatcherConfig
}

// NewHybridMatcher builds a hybrid matcher: trie for exact keyword hits,
// vector for semantic fallback.
func NewHybridMatcher(skills []Skill, cfg MatcherConfig) *HybridMatcher {
	return &HybridMatcher{
		trie:   BuildTriggerIndex(skills),
		vector: NewVectorMatcher(skills, cfg),
		cfg:    cfg,
	}
}

// MatchSkills tries trie first, then falls back to vector search.
// In hybrid mode, the trie result is authoritative but vector adds
// skills the trie misses (up to maxSlots).
func (hm *HybridMatcher) MatchSkills(input string, maxSlots int) []Skill {
	if hm == nil || maxSlots <= 0 {
		return nil
	}

	// Step 1: Trie match (high precision)
	trieMatches := hm.trie.MatchSkills(input, maxSlots)
	if len(trieMatches) >= maxSlots {
		return trieMatches[:maxSlots]
	}

	// Step 2: Vector search (high recall) for what trie missed
	vecMatches := hm.vector.MatchSkills(input, maxSlots)
	if len(vecMatches) == 0 {
		// Vector found nothing either — at least return what trie gave
		if len(trieMatches) > 0 {
			return trieMatches
		}
		return nil
	}

	// Step 3: Merge — take trie matches first, then fill with vector matches
	// Avoid duplicates (same skill name)
	seen := make(map[string]bool, maxSlots)
	var merged []Skill

	for _, s := range trieMatches {
		if !seen[s.Name] {
			seen[s.Name] = true
			merged = append(merged, s)
		}
	}

	for _, s := range vecMatches {
		if !seen[s.Name] {
			seen[s.Name] = true
			merged = append(merged, s)
		}
		if len(merged) >= maxSlots {
			break
		}
	}

	if len(merged) > maxSlots {
		merged = merged[:maxSlots]
	}

	return merged
}

// DebugInfo returns human-readable info about what matched and why.
func (vm *VectorMatcher) DebugInfo(userInput string) string {
	if vm == nil || vm.store == nil || vm.store.Len() == 0 {
		return "matcher is empty"
	}

	queryVec, err := vm.emb.Embed(userInput)
	if err != nil || queryVec == nil {
		return "failed to embed query"
	}

	// Get all results (unlimited)
	k := vm.store.Len()
	results := vm.store.Search(queryVec, k)

	out := fmt.Sprintf("Query: %q\n\nSkill similarities:\n", userInput)
	for _, r := range results {
		sim := 1 - r.Distance
		label := "❌"
		if sim >= vm.cfg.MinSimilarity {
			label = "✅"
		}
		out += fmt.Sprintf("  %s %-25s similarity=%.4f\n", label, r.ID, sim)
	}
	out += fmt.Sprintf("\nThreshold: %.2f\n", vm.cfg.MinSimilarity)
	return out
}

// Ensure interfaces are satisfied
