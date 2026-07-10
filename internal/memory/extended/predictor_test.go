package extended

import (
	"context"
	"testing"
)

func TestPredictorGeneratesIntents(t *testing.T) {
	llm := newMockLLM(`[{"text":"how do I run tests?","confidence":0.85},{"text":"which files changed?","confidence":0.7}]`)
	cfg := DefaultConfig()
	cfg.PredictiveIntents = 3
	p := NewPredictor(llm, cfg)
	intents, err := p.Predict(context.Background(), "Refactor auth package", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 2 {
		t.Fatalf("expected 2 intents, got %d", len(intents))
	}
	if intents[0].Text != "how do I run tests?" {
		t.Errorf("intent text = %q, want how do I run tests?", intents[0].Text)
	}
	if intents[0].Confidence != 0.85 {
		t.Errorf("intent confidence = %f, want 0.85", intents[0].Confidence)
	}
}

func TestPredictorDisabledWhenZero(t *testing.T) {
	llm := newMockLLM()
	cfg := DefaultConfig()
	cfg.PredictiveIntents = 0
	p := NewPredictor(llm, cfg)
	intents, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("expected 0 intents when PredictiveIntents=0, got %d", len(intents))
	}
	if llm.callCount != 0 {
		t.Error("expected no LLM call when disabled")
	}
}

func TestPredictorEmptyResponse(t *testing.T) {
	llm := newMockLLM("[]")
	cfg := DefaultConfig()
	p := NewPredictor(llm, cfg)
	intents, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("expected 0 intents, got %d", len(intents))
	}
}

func TestPredictorNoLLM(t *testing.T) {
	cfg := DefaultConfig()
	p := NewPredictor(nil, cfg)
	intents, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("expected 0 intents when LLM nil, got %d", len(intents))
	}
}

func TestPredictorInvalidJSON(t *testing.T) {
	llm := newMockLLM("not json")
	cfg := DefaultConfig()
	p := NewPredictor(llm, cfg)
	_, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestPredictorCapsIntents(t *testing.T) {
	llm := newMockLLM(`[{"text":"a","confidence":0.1},{"text":"b","confidence":0.2},{"text":"c","confidence":0.3},{"text":"d","confidence":0.4}]`)
	cfg := DefaultConfig()
	cfg.PredictiveIntents = 2
	p := NewPredictor(llm, cfg)
	intents, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 2 {
		t.Errorf("expected 2 intents, got %d", len(intents))
	}
}

func TestPredictorIgnoresEmptyText(t *testing.T) {
	llm := newMockLLM(`[{"text":"","confidence":0.9},{"text":"valid","confidence":0.8}]`)
	cfg := DefaultConfig()
	p := NewPredictor(llm, cfg)
	intents, err := p.Predict(context.Background(), "x", nil, UserState{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(intents) != 1 || intents[0].Text != "valid" {
		t.Errorf("expected [valid], got %v", intents)
	}
}

func TestPredictorNormalizesConfidence(t *testing.T) {
	llm := newMockLLM(`[{"text":"x","confidence":1.5}]`)
	cfg := DefaultConfig()
	p := NewPredictor(llm, cfg)
	intents, _ := p.Predict(context.Background(), "x", nil, UserState{})
	if len(intents) != 1 || intents[0].Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", intents[0].Confidence)
	}
}

func TestPredictorTrimsWhitespace(t *testing.T) {
	llm := newMockLLM(`[{"text":"  ask about tests  ","confidence":0.9}]`)
	cfg := DefaultConfig()
	p := NewPredictor(llm, cfg)
	intents, _ := p.Predict(context.Background(), "x", nil, UserState{})
	if len(intents) != 1 || intents[0].Text != "ask about tests" {
		t.Errorf("expected trimmed text, got %q", intents[0].Text)
	}
}
