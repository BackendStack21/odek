package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// PredictedIntent is a likely follow-up intent generated from the current user
// message and user model.
type PredictedIntent struct {
	Text       string  `json:"text"`
	Confidence float32 `json:"confidence"`
}

// Predictor generates likely follow-up intents from user messages.
type Predictor struct {
	llm LLMClient
	cfg Config
}

// NewPredictor creates a Predictor.
func NewPredictor(llm LLMClient, cfg Config) *Predictor {
	return &Predictor{llm: llm, cfg: cfg}
}

const predictorPrompt = `You are an intent-prediction system. Given the current user message, the recent user messages, and the current user model, predict the most likely follow-up intents.

Current user message:
%s

Recent user messages (JSON):
%s

User model (JSON):
%s

Return ONLY a JSON array of predicted intents. Each intent has a "text" field (a likely follow-up query) and a "confidence" field (0.0-1.0). Example:
[{"text":"how do I run the tests?","confidence":0.85}]

Limit to %d intents. If there is nothing to predict, return [].`

// Predict returns up to PredictiveIntents likely follow-up intents. It returns
// an empty slice when prediction is disabled or the LLM is unavailable.
func (p *Predictor) Predict(ctx context.Context, userMsg string, recent []string, state UserState) ([]PredictedIntent, error) {
	if p.llm == nil || p.cfg.PredictiveIntents <= 0 {
		return nil, nil
	}
	recentJSON, err := json.Marshal(recent)
	if err != nil {
		return nil, fmt.Errorf("predictor: marshal recent: %w", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("predictor: marshal state: %w", err)
	}
	prompt := fmt.Sprintf(predictorPrompt, userMsg, string(recentJSON), string(stateJSON), p.cfg.PredictiveIntents)
	resp, err := p.llm.SimpleCall(ctx,
		"You are an intent-prediction system. Return only a JSON array of predicted intents.",
		prompt,
	)
	if err != nil {
		log.Printf("extended memory: predictor LLM failed: %v", err)
		return nil, fmt.Errorf("predictor: %w", err)
	}
	resp = strings.TrimSpace(resp)
	if resp == "" || resp == "[]" {
		return nil, nil
	}
	var raw []struct {
		Text       string  `json:"text"`
		Confidence float32 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		log.Printf("extended memory: predictor parse failed: %v", err)
		return nil, fmt.Errorf("predictor: parse: %w", err)
	}
	intents := make([]PredictedIntent, 0, len(raw))
	for _, r := range raw {
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		if r.Confidence < 0 {
			r.Confidence = 0
		} else if r.Confidence > 1.0 {
			r.Confidence = 1.0
		}
		intents = append(intents, PredictedIntent{Text: text, Confidence: r.Confidence})
	}
	if len(intents) > p.cfg.PredictiveIntents {
		intents = intents[:p.cfg.PredictiveIntents]
	}
	return intents, nil
}
