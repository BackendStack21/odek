package skills

import (
	"strings"
)

// ── Keyword Derivation ────────────────────────────────────────────────
//
// Derives topic and action keywords from a skill's body text using
// simple word frequency analysis. This runs when a skill has no explicit
// trigger defined — e.g. self-improvement auto-generated skills.

// scoredWord holds a word and its derivation score.
type scoredWord struct {
	word  string
	score float64
}

// DeriveKeywords extracts topic and action keywords from body text.
// Uses word frequency + heuristic POS detection.
// Returns (topics, actions) slices.
func DeriveKeywords(body string) ([]string, []string) {
	words := tokenize(body)
	if len(words) < 3 {
		return extractFromHeadwords(words), nil
	}

	// Count word frequencies
	freq := make(map[string]int)
	for _, w := range words {
		freq[w]++
	}

	// Score each word: frequency × POS heuristic weight
	var scoredWords []scoredWord

	seen := make(map[string]bool)
	for _, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true

		f := freq[w]
		// Normalize by total words for small documents
		score := float64(f) / float64(len(words))
		pos := probePOS(w)
		score *= pos.weight

		scoredWords = append(scoredWords, scoredWord{word: w, score: score})
	}

	// Sort by score descending
	sortScored(scoredWords)

	// Split into topic vs action
	var topics, actions []string
	for _, s := range scoredWords {
		pos := probePOS(s.word)
		if pos.isAction {
			actions = append(actions, s.word)
		} else {
			topics = append(topics, s.word)
		}
	}

	// Cap at reasonable limits
	if len(topics) > 10 {
		topics = topics[:10]
	}
	if len(actions) > 5 {
		actions = actions[:5]
	}

	return topics, actions
}

type posInfo struct {
	isAction bool
	weight   float64
}

func probePOS(word string) posInfo {
	// Heuristic: words ending in common verb suffixes are likely actions
	if strings.HasSuffix(word, "ing") ||
		strings.HasSuffix(word, "ed") ||
		strings.HasSuffix(word, "ify") ||
		strings.HasSuffix(word, "ize") ||
		strings.HasSuffix(word, "ise") ||
		strings.HasSuffix(word, "ate") ||
		word == "build" || word == "run" || word == "make" ||
		word == "test" || word == "deploy" || word == "debug" ||
		word == "install" || word == "configure" || word == "fix" ||
		word == "review" || word == "write" || word == "create" ||
		word == "optimize" || word == "check" || word == "validate" {
		return posInfo{isAction: true, weight: 1.5}
	}
	return posInfo{isAction: false, weight: 1.0}
}

// extractFromHeadwords returns the first few words as topics when
// the body is too short for frequency analysis.
func extractFromHeadwords(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	limit := 5
	if len(words) < limit {
		limit = len(words)
	}
	return words[:limit]
}

// sortScored sorts scored words by score descending.
func sortScored(items []scoredWord) {
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

// ── Stopword Helpers ──────────────────────────────────────────────────

// IsStopword returns true if the word is a common English stopword.
func IsStopword(word string) bool {
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
	return stopwords[word]
}
