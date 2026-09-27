package extended

import (
	"context"
	"strings"
	"testing"
)

// TestExtractorEnforcesTypeQuotas pins that a single extraction run cannot
// mint an unbounded number of atoms of one type: quality-ranked quota per
// type plus an overall per-run cap. A model that emits 6 goal atoms in one
// run must not store them all.
func TestExtractorEnforcesTypeQuotas(t *testing.T) {
	mk := func(typ string) string {
		return `{"text":"User goal ` + typ + ` number placeholder unique","type":"` + typ + `","confidence":0.9}`
	}
	var items []string
	for i := 0; i < 6; i++ {
		items = append(items, strings.Replace(mk("goal"), "placeholder", string(rune('a'+i)), 1))
	}
	for i := 0; i < 6; i++ {
		items = append(items, strings.Replace(mk("fact"), "placeholder", string(rune('a'+i)), 1))
	}
	resp := `[` + strings.Join(items, ",") + `]`
	llm := newMockLLM(resp)
	ex := NewExtractor(llm, DefaultConfig())
	atoms, err := ex.Extract(context.Background(), "lots of things")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	goals, facts := 0, 0
	for _, a := range atoms {
		switch a.Type {
		case TypeGoal:
			goals++
		case TypeFact:
			facts++
		}
	}
	if goals > ExtractionTypeQuota {
		t.Errorf("goal atoms = %d, want <= %d", goals, ExtractionTypeQuota)
	}
	if facts > ExtractionTypeQuota {
		t.Errorf("fact atoms = %d, want <= %d", facts, ExtractionTypeQuota)
	}
	if len(atoms) > ExtractionRunCap {
		t.Errorf("total atoms = %d, want <= %d", len(atoms), ExtractionRunCap)
	}
}

// TestApplyExtractionQuotasSmallBatchPassthrough pins that a batch within
// both caps passes through untouched, in original order.
func TestApplyExtractionQuotasSmallBatchPassthrough(t *testing.T) {
	atoms := []MemoryAtom{
		{Text: "a", Type: TypeFact, Confidence: 0.1},
		{Text: "b", Type: TypeFact, Confidence: 0.9},
	}
	got := applyExtractionQuotas(atoms)
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Errorf("small batch must pass through in order, got %+v", got)
	}
}

// TestExtractorTypeQuotaKeepsHighestConfidence pins that quota trimming is
// quality-ranked: when over quota, the highest-confidence atoms survive.
func TestExtractorTypeQuotaKeepsHighestConfidence(t *testing.T) {
	resp := `[` + strings.Join([]string{
		`{"text":"low confidence goal","type":"goal","confidence":0.3}`,
		`{"text":"high confidence goal","type":"goal","confidence":0.95}`,
		`{"text":"mid confidence goal","type":"goal","confidence":0.6}`,
		`{"text":"another high goal","type":"goal","confidence":0.9}`,
	}, ",") + `]`
	llm := newMockLLM(resp)
	ex := NewExtractor(llm, DefaultConfig())
	atoms, err := ex.Extract(context.Background(), "goals")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if len(atoms) != ExtractionTypeQuota {
		t.Fatalf("expected %d atoms after quota, got %d", ExtractionTypeQuota, len(atoms))
	}
	kept := map[string]bool{}
	for _, a := range atoms {
		kept[a.Text] = true
	}
	if !kept["high confidence goal"] || !kept["another high goal"] {
		t.Errorf("quota must keep the highest-confidence atoms, kept %v", kept)
	}
	if kept["low confidence goal"] {
		t.Errorf("quota must drop the lowest-confidence atom first, kept %v", kept)
	}
}
