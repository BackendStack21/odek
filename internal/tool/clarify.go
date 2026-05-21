// Package tool provides the clarify tool — ask the user a question and
// wait for a response. The tool blocks until a response arrives via an
// injected answer function.
package tool

import (
	"encoding/json"
	"fmt"
)

// ── Types ──────────────────────────────────────────────────────────────

// ClarifyTool implements the agent tool interface for asking the user
// questions during a task. The actual delivery and response collection
// is handled by an injected AnswerFunc (platform-specific).
type ClarifyTool struct {
	// Answer is called with the question text. The returned string is the
	// user's response, or an error if no answer was received (timeout,
	// cancellation, etc.). This function blocks until the user responds.
	Answer func(question string) (string, error)
}

// ClarifyArgs is the JSON schema for clarify tool arguments.
type ClarifyArgs struct {
	Question string `json:"question"` // question to ask the user
}

// ── Tool interface ─────────────────────────────────────────────────────

func (t *ClarifyTool) Name() string { return "clarify" }

func (t *ClarifyTool) Description() string {
	return `Ask the user a question when you need clarification or a decision.
Use this when:
- The task is ambiguous and you need the user to choose an approach
- You're stuck and need guidance before continuing
- You need to confirm a destructive or irreversible action
The tool will pause execution and wait for the user's response.`
}

func (t *ClarifyTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "The question to ask the user. Be clear and concise. Include any relevant context or options.",
			},
		},
		"required": []string{"question"},
	}
}

// Call blocks until the user provides an answer or an error occurs.
// The question is delivered to the user via the injected Answer function
// (e.g., via Telegram message, CLI prompt, or web UI).
func (t *ClarifyTool) Call(argsJSON string) (string, error) {
	var args ClarifyArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("clarify: parse args: %w", err)
	}
	if args.Question == "" {
		return "", fmt.Errorf("clarify: question is required")
	}
	if t.Answer == nil {
		return "", fmt.Errorf("clarify: Answer function not set (platform must wire it)")
	}

	answer, err := t.Answer(args.Question)
	if err != nil {
		return "", fmt.Errorf("clarify: %w", err)
	}
	return answer, nil
}

// ── Convenience ────────────────────────────────────────────────────────

// NewClarifyTool creates a ClarifyTool with the given answer function.
// The answer function is platform-specific — it delivers the question to
// the user and returns their response (blocking until available).
func NewClarifyTool(answer func(question string) (string, error)) *ClarifyTool {
	return &ClarifyTool{Answer: answer}
}
