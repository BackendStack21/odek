package embedding

import (
	"strings"
	"unicode"
)

// normalizeForEmbedding lowercases text and reduces it to space-separated
// alphanumeric tokens. This ensures go-vector's bag-of-words featurization is
// not fragmented by punctuation or casing, so "Postgres," and "postgres" hash
// to the same RP token.
func normalizeForEmbedding(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inWord := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			inWord = true
		} else if inWord {
			b.WriteByte(' ')
			inWord = false
		}
	}
	return strings.TrimSpace(b.String())
}

// featurizeForEmbedding normalizes text and appends adjacent-word bigram tokens
// ("w1_w2") so the RP embedder captures a little local word order. For example,
// "uses postgres" and "uses mysql" share the token "uses" but differ on the
// bigrams "uses_postgres" vs "uses_mysql", giving RP a stronger signal to
// distinguish them than plain BoW alone.
//
// Corpus texts AND query texts MUST both pass through this function so their
// vectors live in the same feature space.
func featurizeForEmbedding(text string) string {
	norm := normalizeForEmbedding(text)
	if norm == "" {
		return norm
	}
	words := strings.Fields(norm)
	if len(words) < 2 {
		return norm
	}
	out := make([]string, 0, len(words)*2-1)
	out = append(out, words...)
	for i := 0; i+1 < len(words); i++ {
		out = append(out, words[i]+"_"+words[i+1])
	}
	return strings.Join(out, " ")
}

// featurizeAll applies featurizeForEmbedding to every element of a slice.
// Used when building the RP corpus for Fit.
func featurizeAll(texts []string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = featurizeForEmbedding(t)
	}
	return out
}
