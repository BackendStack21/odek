// Package skills — advanced skill matching using scoring-based approach.
//
// Replaces the brittle AND-lock (topic AND action both required) with a
// scoring system:
//   - Exact keyword match (topic) → +3 points
//   - Exact keyword match (action) → +3 points
//   - Prefix / morphological match → +2 points  (e.g., "debug" matches "debugging")
//   - Description token match → +1 point
//   - Synonym match → +2 points  (e.g., "improve" ↔ "optimize")
//
// A skill loads if its total score >= threshold (default: 3).
// This means a single topic keyword match (3 pts) is enough — no AND-lock.
// But two partial matches (2+2) also work.
package skills

import (
	"sort"
	"strconv"
	"strings"
)

// ScoredMatcherConfig controls the scored matcher behavior.
type ScoredMatcherConfig struct {
	MinScore       int  `json:"min_score"`       // minimum total score to load (default 3)
	TopicWeight    int  `json:"topic_weight"`    // exact topic match score (default 3)
	ActionWeight   int  `json:"action_weight"`   // exact action match score (default 3)
	PrefixWeight   int  `json:"prefix_weight"`   // prefix/substring match score (default 2)
	DescWeight     int  `json:"desc_weight"`     // description token match score (default 1)
	SynonymWeight  int  `json:"synonym_weight"`  // synonym match score (default 2)
	MaxResults     int  `json:"max_results"`     // max skills returned (default 5)
	EnableSynonyms bool `json:"enable_synonyms"` // use synonym expansion (default true)
	EnableStemming bool `json:"enable_stemming"` // use simple suffix-stripping (default true)
}

// DefaultScoredConfig returns sensible defaults.
func DefaultScoredConfig() ScoredMatcherConfig {
	return ScoredMatcherConfig{
		MinScore:       3,
		TopicWeight:    3,
		ActionWeight:   3,
		PrefixWeight:   2,
		DescWeight:     1,
		SynonymWeight:  2,
		MaxResults:     5,
		EnableSynonyms: true,
		EnableStemming: true,
	}
}

// ScoredMatcher matches skills using a scoring system instead of AND-lock.
type ScoredMatcher struct {
	skills []Skill
	prep   []skillTerms // parallel to skills
	cfg    ScoredMatcherConfig
	syn    *synonymMap
}

// skillTerms holds the per-skill lookup sets derived once from a skill's
// trigger keywords and description, so scoring a message does not rebuild them.
type skillTerms struct {
	topic  map[string]bool
	action map[string]bool
	desc   map[string]bool
}

func prepareSkillTerms(s Skill) skillTerms {
	t := skillTerms{
		topic:  toLowerSet(s.Trigger.TopicKeywords),
		action: toLowerSet(s.Trigger.ActionKeywords),
	}
	if s.Description != "" {
		toks := tokenize(s.Description)
		t.desc = make(map[string]bool, len(toks))
		for _, dt := range toks {
			t.desc[strings.ToLower(dt)] = true
		}
	}
	return t
}

// NewScoredMatcher builds a scored matcher from a list of lazy skills.
func NewScoredMatcher(skills []Skill, cfg ScoredMatcherConfig) *ScoredMatcher {
	if cfg.MinScore <= 0 {
		cfg.MinScore = DefaultScoredConfig().MinScore
	}
	if cfg.TopicWeight <= 0 {
		cfg.TopicWeight = DefaultScoredConfig().TopicWeight
	}
	if cfg.ActionWeight <= 0 {
		cfg.ActionWeight = DefaultScoredConfig().ActionWeight
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = DefaultScoredConfig().MaxResults
	}
	if cfg.PrefixWeight <= 0 {
		cfg.PrefixWeight = DefaultScoredConfig().PrefixWeight
	}

	prep := make([]skillTerms, len(skills))
	for i, sk := range skills {
		prep[i] = prepareSkillTerms(sk)
	}
	return &ScoredMatcher{
		skills: skills,
		prep:   prep,
		cfg:    cfg,
		syn:    newSynonymMap(),
	}
}

// MatchSkills returns skills matching the user input, scored and ranked.
// This fixes the AND-lock problem: any combination of matches can trigger.
func (sm *ScoredMatcher) MatchSkills(input string, maxSlots int) []Skill {
	if sm == nil || len(sm.skills) == 0 || maxSlots <= 0 {
		return nil
	}

	tokens := tokenize(input)
	if len(tokens) == 0 {
		return nil
	}

	// Propagage maxSlots from caller but cap at cfg.MaxResults
	k := maxSlots
	if sm.cfg.MaxResults > 0 && k > sm.cfg.MaxResults {
		k = sm.cfg.MaxResults
	}

	type scoredSkill struct {
		skill Skill
		score int
	}

	var results []scoredSkill

	for i, s := range sm.skills {
		score := sm.scoreSkillWith(s, sm.terms(i, s), tokens)
		if score >= sm.cfg.MinScore {
			results = append(results, scoredSkill{skill: s, score: score})
		}
	}

	if len(results) == 0 {
		return nil
	}

	// Sort by score descending, then by name for determinism
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].skill.Name < results[j].skill.Name
	})

	if len(results) > k {
		results = results[:k]
	}

	out := make([]Skill, len(results))
	for i, rs := range results {
		out[i] = rs.skill
	}
	return out
}

// scoreSkill computes a match score for a single skill against user tokens.
func (sm *ScoredMatcher) scoreSkill(s Skill, userTokens []string) int {
	return sm.scoreSkillWith(s, prepareSkillTerms(s), userTokens)
}

// terms returns the prepared lookup sets for sm.skills[i], deriving them on
// the fly for a matcher whose skills were set without NewScoredMatcher.
func (sm *ScoredMatcher) terms(i int, s Skill) skillTerms {
	if i < len(sm.prep) {
		return sm.prep[i]
	}
	return prepareSkillTerms(s)
}

// scoreSkillWith scores a skill using its prepared keyword and description sets.
func (sm *ScoredMatcher) scoreSkillWith(s Skill, terms skillTerms, userTokens []string) int {
	score := 0
	topicSet, actionSet := terms.topic, terms.action

	for _, tok := range userTokens {
		// 1. Exact topic match → +TopicWeight
		if topicSet[tok] {
			score += sm.cfg.TopicWeight
		}
		// 2. Exact action match → +ActionWeight
		if actionSet[tok] {
			score += sm.cfg.ActionWeight
		}
		// 3. Prefix / stem match on topic keywords → +PrefixWeight
		if sm.cfg.EnableStemming || sm.cfg.PrefixWeight > 0 {
			if !topicSet[tok] { // skip if already matched exactly above
				for _, kw := range s.Trigger.TopicKeywords {
					kwl := strings.ToLower(kw)
					if hasCommonStem(tok, kwl) {
						score += sm.cfg.PrefixWeight
						break
					}
				}
			}
			if !actionSet[tok] {
				for _, kw := range s.Trigger.ActionKeywords {
					kwl := strings.ToLower(kw)
					if hasCommonStem(tok, kwl) {
						score += sm.cfg.PrefixWeight
						break
					}
				}
			}
		}
		// 4. Synonym match → +SynonymWeight
		if sm.cfg.EnableSynonyms && sm.syn != nil {
			if !topicSet[tok] {
				for _, kw := range s.Trigger.TopicKeywords {
					if sm.syn.areSynonyms(tok, strings.ToLower(kw)) {
						score += sm.cfg.SynonymWeight
						break
					}
				}
			}
			if !actionSet[tok] {
				for _, kw := range s.Trigger.ActionKeywords {
					if sm.syn.areSynonyms(tok, strings.ToLower(kw)) {
						score += sm.cfg.SynonymWeight
						break
					}
				}
			}
		}
		// 5. Description token match → +DescWeight
		if sm.cfg.DescWeight > 0 && terms.desc[tok] {
			score += sm.cfg.DescWeight
		}
	}

	return score
}

// ── Stemming ────────────────────────────────────────────────────────────

// hasCommonStem returns true if two words share a common stem via simple
// suffix stripping. Handles common English morphological variants.
func hasCommonStem(a, b string) bool {
	if a == b {
		return true
	}
	// Check prefix overlap: if one is the prefix of the other (trie-like)
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	return stripSuffix(a) == stripSuffix(b)
}

// stripSuffix removes common English suffixes for lightweight stemming.
func stripSuffix(word string) string {
	// Don't strip very short words
	if len(word) <= 4 {
		return word
	}
	// Try removing suffixes in order (longest first to avoid over-stripping)
	suffixes := []string{
		"ization", "isation", "inator", // optimization → optim
		"ifying", "izing", "ising", // optimizing → optim
		"ified", "ized", "ised", // optimized → optim
		"ize", "ise", // optimize → optim
		"ifier", "izer", "iser", // optimizer → optim
		"ations", "atives", // (rare)
		"ation", "ative", "ature", // optimization → optimiz? no...
		"ement",                              // deployment → deploy
		"ness",                               // darkness → dark
		"less",                               // useless → use
		"ment", "able", "ible", "ing", "ion", // building → build, action → act
		"tion", "sion", // optimization → optimiza... no
		"ed", "er", "or", "ly", "es", "s", // debugged → debug, debugger → debug
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(word, suf) && len(word)-len(suf) >= 3 {
			return word[:len(word)-len(suf)]
		}
	}
	return word
}

// ── Synonyms ────────────────────────────────────────────────────────────

// synonymMap provides lightweight synonym expansion for common tech terms.
type synonymMap struct {
	groups [][]string     // synonym groups
	lookup map[string]int // word → group index
}

func newSynonymMap() *synonymMap {
	groups := [][]string{
		// Build / compile / construct
		{"build", "compile", "construct", "make", "create", "generate", "produce", "bundle"},
		// Deploy / release / ship
		{"deploy", "release", "ship", "publish", "rollout", "launch", "deliver"},
		// Test / validate / verify
		{"test", "validate", "verify", "check", "assert", "inspect", "audit"},
		// Debug / fix / repair
		{"debug", "fix", "repair", "troubleshoot", "diagnose", "resolve", "patch"},
		// Optimize / improve / tune
		{"optimize", "improve", "tune", "enhance", "refactor", "streamline", "boost"},
		// Configure / setup / install
		{"configure", "setup", "install", "initialize", "prepare", "bootstrap"},
		// Monitor / observe / watch
		{"monitor", "observe", "watch", "track", "trace", "log", "metrics"},
		// Search / find / query
		{"search", "find", "query", "lookup", "locate", "discover"},
		// Analyze / study / review
		{"analyze", "analyse", "study", "review", "examine", "inspect", "investigate"},
		// Delete / remove / clean
		{"delete", "remove", "clean", "purge", "wipe", "clear", "uninstall"},
		// Migrate / move / transfer
		{"migrate", "move", "transfer", "relocate", "port", "convert"},
		// Document / explain / describe
		{"document", "explain", "describe", "summarize", "outline"},
		// Update / upgrade / refresh
		{"update", "upgrade", "refresh", "renew", "modernize"},
		// Connect / link / integrate
		{"connect", "link", "integrate", "attach", "mount", "bind"},
		// Scale / grow / expand
		{"scale", "grow", "expand", "extend", "multiply"},
		// Secure / protect / harden
		{"secure", "protect", "harden", "encrypt", "authenticate", "authorize"},
		// Backup / restore / recover
		{"backup", "restore", "recover", "archive", "save"},
		// Format / lint / style
		{"format", "lint", "style", "prettify", "beautify"},
	}

	lookup := make(map[string]int)
	for gi, group := range groups {
		for _, word := range group {
			lookup[word] = gi
		}
	}

	return &synonymMap{groups: groups, lookup: lookup}
}

// areSynonyms returns true if word a and word b are in the same synonym group.
func (s *synonymMap) areSynonyms(a, b string) bool {
	if a == b {
		return true
	}
	giA, okA := s.lookup[a]
	giB, okB := s.lookup[b]
	if !okA || !okB {
		return false
	}
	return giA == giB
}

// ── Helpers ─────────────────────────────────────────────────────────────

func toLowerSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[strings.ToLower(item)] = true
	}
	return set
}

// ── Debugging ───────────────────────────────────────────────────────────

// ExplainMatch returns a human-readable explanation of why skills matched.
func (sm *ScoredMatcher) ExplainMatch(input string) string {
	if sm == nil || len(sm.skills) == 0 {
		return "no skills loaded"
	}

	tokens := tokenize(input)
	var b strings.Builder
	b.WriteString("Input tokens: ")
	b.WriteString(strings.Join(tokens, ", "))
	b.WriteString("\n\n")

	for i, s := range sm.skills {
		score := sm.scoreSkillWith(s, sm.terms(i, s), tokens)
		status := "❌"
		if score >= sm.cfg.MinScore {
			status = "✅"
		}
		b.WriteString(status)
		b.WriteString(" ")
		b.WriteString(s.Name)
		b.WriteString(" (score=")
		b.WriteString(strconv.Itoa(score))
		b.WriteString("/")
		b.WriteString(strconv.Itoa(sm.cfg.MinScore))
		b.WriteString(")")

		// Show keyword hits
		allKWs := append(s.Trigger.TopicKeywords, s.Trigger.ActionKeywords...)
		for _, tok := range tokens {
			for _, kw := range allKWs {
				if strings.ToLower(kw) == tok {
					b.WriteString(" [exact:")
					b.WriteString(kw)
					b.WriteString("]")
				} else if hasCommonStem(tok, strings.ToLower(kw)) {
					b.WriteString(" [stem:")
					b.WriteString(tok)
					b.WriteString("~")
					b.WriteString(kw)
					b.WriteString("]")
				} else if sm.syn.areSynonyms(tok, strings.ToLower(kw)) {
					b.WriteString(" [syn:")
					b.WriteString(tok)
					b.WriteString("~")
					b.WriteString(kw)
					b.WriteString("]")
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
