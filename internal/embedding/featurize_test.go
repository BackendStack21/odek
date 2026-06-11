package embedding

import (
	"strings"
	"testing"
)

func TestNormalizeForEmbedding(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Postgres,", "postgres"},
		{"uses Postgres.", "uses postgres"},
		{"  hello  world  ", "hello world"},
		{"", ""},
		{"123abc!", "123abc"},
		{"go test ./...", "go test"},
	}
	for _, c := range cases {
		got := normalizeForEmbedding(c.in)
		if got != c.want {
			t.Errorf("normalizeForEmbedding(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeaturizeForEmbedding_BigramsPresent(t *testing.T) {
	out := featurizeForEmbedding("uses postgres")
	if !strings.Contains(out, "uses_postgres") {
		t.Errorf("expected bigram 'uses_postgres' in %q", out)
	}
	if !strings.Contains(out, "uses") || !strings.Contains(out, "postgres") {
		t.Errorf("expected unigrams in %q", out)
	}
}

func TestFeaturizeForEmbedding_SingleWordNoBigrams(t *testing.T) {
	out := featurizeForEmbedding("postgres")
	if strings.Contains(out, "_") {
		t.Errorf("single word should not produce bigrams, got %q", out)
	}
}

func TestFeaturizeForEmbedding_Empty(t *testing.T) {
	if got := featurizeForEmbedding(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

func TestFeaturizeAll(t *testing.T) {
	texts := []string{"Uses Postgres", "runs go test"}
	got := featurizeAll(texts)
	if len(got) != 2 {
		t.Fatalf("featurizeAll: want 2 results, got %d", len(got))
	}
	if !strings.Contains(got[0], "uses_postgres") {
		t.Errorf("featurizeAll[0]: expected bigram, got %q", got[0])
	}
	if !strings.Contains(got[1], "runs_go") {
		t.Errorf("featurizeAll[1]: expected bigram, got %q", got[1])
	}
}
