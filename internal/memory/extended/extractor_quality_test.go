package extended

import (
	"context"
	"strings"
	"testing"
)

// TestQualityViolationDetectsProvenanceInText pins the write-time quality
// validator: atom text that embeds provenance tokens (session IDs, turn
// numbers) or release ephemera (PR numbers, commit hashes, version tags)
// must be flagged so the extractor can drop it.
func TestQualityViolationDetectsProvenanceInText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"session id", "User confirmed merge in session 20260918-3e4cb01f", true},
		{"turn number parenthetical", "User said merge after CI (turn 3)", true},
		{"turn number comma", "decided, at turn 12, to proceed", true},
		{"turn prose not provenance", "per turn 100 requests are allowed", false},
		{"pr number", "PR #45 was merged", true},
		{"pr number lowercase", "pr #45 was merged", true},
		{"commit hash", "squash-merged as 768d380 on main", true},
		{"version tag", "the correct version tag is 1.14.8", true},
		{"version tag v prefix", "release v1.42.3 shipped", true},
		{"semver bare", "shipped v1.42.1 yesterday", true},
		{"go version not a tag", "User works with Go 1.24", false},
		{"pending_review self-reference", "consume/resolve pending_review entries 555af9bf", true},
		{"already stored restatement", "already stored, no change: user prefers concise answers", true},

		{"clean preference", "User prefers concise answers", false},
		{"clean convention", "User requires CI checks to pass before any merge", false},
		{"clean fact", "User maintains odek under the BackendStack21 organization", false},
		{"number that is not a tag", "User keeps sub-agent concurrency capped at 2", false},
		{"hash-like word", "User likes the hashing approach", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := qualityViolation(tc.text)
			if got != tc.want {
				t.Errorf("qualityViolation(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestExtractorDropsProvenanceAtoms pins the extractor contract: even when
// the LLM returns atoms with provenance or release ephemera baked into the
// text, Extract must not emit them. The prompt is a mitigation, not the
// mechanism — this filter is.
func TestExtractorDropsProvenanceAtoms(t *testing.T) {
	resp := `[` + strings.Join([]string{
		`{"text":"User confirmed merge in session 20260918-3e4cb01f","type":"fact","confidence":0.9}`,
		`{"text":"PR #45 was squash-merged as 768d380","type":"fact","confidence":0.8}`,
		`{"text":"The correct version tag is 1.14.8","type":"fact","confidence":0.9}`,
		`{"text":"Consume pending_review entries 555af9bf","type":"goal","confidence":0.85}`,
		`{"text":"User requires CI to pass before any merge","type":"convention","confidence":0.95}`,
	}, ",") + `]`
	llm := newMockLLM(resp)
	ex := NewExtractor(llm, DefaultConfig())
	atoms, err := ex.Extract(context.Background(), "we merged it")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if len(atoms) != 1 {
		t.Fatalf("expected 1 surviving atom, got %d: %+v", len(atoms), atoms)
	}
	if !strings.Contains(atoms[0].Text, "CI to pass") {
		t.Errorf("surviving atom should be the generalizing one, got %q", atoms[0].Text)
	}
}

// TestExtractionPromptBansNoise pins that the prompt itself carries the
// negative guidance (example-first rejects), so the model is nudged before
// the write-time filter has to drop anything.
func TestExtractionPromptBansNoise(t *testing.T) {
	for _, want := range []string{
		"Do NOT extract",
		"session ID",
		"turn number",
		"version",
		"PR",
		"REJECT",
	} {
		if !strings.Contains(extractionPrompt, want) {
			t.Errorf("extractionPrompt missing negative-example guidance %q", want)
		}
	}
}
