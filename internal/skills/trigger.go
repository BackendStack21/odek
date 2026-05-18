package skills

import (
	"sort"
	"strings"
)

// ── Keyword Trie ───────────────────────────────────────────────────────
//
// The trie maps individual keywords to the skills that have them.
// Input tokenization + trie lookup is O(input_words) per turn, not
// O(skills × input_words). This avoids testing N regexes per turn.

// trieNode is a node in the keyword trie.
type trieNode struct {
	skills []int // indices into the skills slice that match this keyword
	child  map[string]*trieNode
}

// triggerIndex is the compiled index used for fast matching.
type triggerIndex struct {
	topicRoot  *trieNode
	actionRoot *trieNode
	skills     []Skill
}

// BuildTriggerIndex builds a keyword trie from a list of skills.
// Each skill's topic and action keywords are indexed separately.
func BuildTriggerIndex(skills []Skill) *triggerIndex {
	idx := &triggerIndex{
		topicRoot:  &trieNode{child: make(map[string]*trieNode)},
		actionRoot: &trieNode{child: make(map[string]*trieNode)},
		skills:     skills,
	}

	for i, s := range skills {
		for _, kw := range s.Trigger.TopicKeywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" {
				continue
			}
			insertTrie(idx.topicRoot, kw, i)
		}
		for _, kw := range s.Trigger.ActionKeywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" {
				continue
			}
			insertTrie(idx.actionRoot, kw, i)
		}
	}

	return idx
}

func insertTrie(root *trieNode, word string, skillIdx int) {
	node := root
	for _, ch := range word {
		char := string(ch)
		if node.child == nil {
			node.child = make(map[string]*trieNode)
		}
		next, ok := node.child[char]
		if !ok {
			next = &trieNode{child: make(map[string]*trieNode)}
			node.child[char] = next
		}
		node = next
	}
	node.skills = append(node.skills, skillIdx)
}

// ── Matching ──────────────────────────────────────────────────────────

// MatchSkills returns the skills whose topic AND action keywords match
// the given user input. Results are capped at maxSlots, ordered by
// skill source priority (project > user > extra), then alphabetically.
func (idx *triggerIndex) MatchSkills(input string, maxSlots int) []Skill {
	if idx == nil || maxSlots <= 0 {
		return nil
	}

	// Tokenize input: lowercase, split on whitespace and common punctuation
	tokens := tokenize(input)
	if len(tokens) == 0 {
		return nil
	}

	// Find skills matching topic keywords
	topicMatches := make(map[int]bool) // skill index → matched
	for _, tok := range tokens {
		for _, si := range lookupTrie(idx.topicRoot, tok) {
			topicMatches[si] = true
		}
	}

	if len(topicMatches) == 0 {
		return nil
	}

	// Find skills matching action keywords (subset of topic matches)
	actionMatches := make(map[int]bool)
	for _, tok := range tokens {
		for _, si := range lookupTrie(idx.actionRoot, tok) {
			if topicMatches[si] {
				actionMatches[si] = true
			}
		}
	}

	if len(actionMatches) == 0 {
		return nil
	}

	// Sort by priority (project dir first, then user, then extra)
	matched := make([]Skill, 0, len(actionMatches))
	for si := range actionMatches {
		matched = append(matched, idx.skills[si])
	}

	// Stable sort: source priority, then by name
	sort.SliceStable(matched, func(i, j int) bool {
		// Project dir takes priority over user dir
		pi := dirPriority(matched[i].Source.Dir)
		pj := dirPriority(matched[j].Source.Dir)
		if pi != pj {
			return pi < pj // lower number = higher priority
		}
		return matched[i].Name < matched[j].Name
	})

	if len(matched) > maxSlots {
		matched = matched[:maxSlots]
	}

	return matched
}

// lookupTrie finds all skill indices that match a single word.
func lookupTrie(root *trieNode, word string) []int {
	if root == nil {
		return nil
	}

	seen := make(map[int]bool)
	collect := func(indices []int) {
		for _, si := range indices {
			seen[si] = true
		}
	}

	// Exact match
	node := root
	matched := true
	for _, ch := range word {
		char := string(ch)
		next, ok := node.child[char]
		if !ok {
			matched = false
			break
		}
		node = next
	}
	if matched {
		collect(node.skills)
		// Also collect all descendants (prefix match)
		collectDescendants(node, collect)
	}

	return keys(seen)
}

func collectDescendants(node *trieNode, collect func([]int)) {
	for _, child := range node.child {
		collect(child.skills)
		collectDescendants(child, collect)
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// dirPriority returns a sort key for skill directories (lower = higher priority).
func dirPriority(dir string) int {
	// Project dir: ./.kode/skills or starts with a non-home path ending in /.kode/skills
	if dir == ".kode/skills" || strings.HasPrefix(dir, "./") && strings.Contains(dir, ".kode/skills") {
		return 0
	}
	// User dir: ~/.kode/skills or /home/*/.kode/skills or any path with /.kode/skills
	if strings.Contains(dir, ".kode/skills") {
		return 1
	}
	return 2
}

// tokenize splits input into lowercase words, removing common punctuation.
func tokenize(input string) []string {
	input = strings.ToLower(input)

	// Replace punctuation with spaces
	replacer := strings.NewReplacer(
		".", " ", ",", " ", "!", " ", "?", " ", ";", " ", ":", " ",
		"'", " ", "\"", " ", "(", " ", ")", " ", "[", " ", "]", " ",
		"{", " ", "}", " ", "/", " ", "\\", " ", "`", " ",
		"\n", " ", "\t", " ",
	)
	cleaned := replacer.Replace(input)

	words := strings.Fields(cleaned)

	// Filter stopwords
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "shall": true,
		"this": true, "that": true, "these": true, "those": true,
		"i": true, "you": true, "he": true, "she": true, "it": true,
		"we": true, "they": true, "me": true, "him": true, "her": true,
		"us": true, "them": true, "my": true, "your": true, "his": true,
		"its": true, "our": true, "their": true, "mine": true, "yours": true,
		"hers": true, "theirs": true, "and": true, "but": true, "or": true,
		"nor": true, "not": true, "if": true, "then": true, "else": true,
		"of": true, "at": true, "by": true, "for": true, "with": true,
		"about": true, "into": true, "through": true, "during": true,
		"before": true, "after": true, "above": true, "below": true,
		"to": true, "from": true, "up": true, "down": true, "in": true,
		"out": true, "on": true, "off": true, "over": true, "under": true,
		"again": true, "further": true, "once": true, "here": true,
		"there": true, "when": true, "where": true, "why": true, "how": true,
		"all": true, "each": true, "every": true, "both": true, "few": true,
		"more": true, "most": true, "other": true, "some": true, "such": true,
		"no": true, "only": true, "own": true, "same": true, "so": true,
		"than": true, "too": true, "very": true, "just": true,
		"because": true, "as": true, "until": true, "while": true,
		"please": true, "help": true, "need": true, "want": true,
		"like": true, "get": true, "make": true, "take": true, "use": true,
		"put": true, "set": true, "let": true, "come": true,
		"see": true, "know": true, "think": true, "tell": true,
	}

	var out []string
	for _, w := range words {
		if !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}
