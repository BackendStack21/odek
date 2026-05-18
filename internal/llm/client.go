// Package llm provides an OpenAI-compatible HTTP client using only stdlib.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends chat completion requests to any OpenAI-compatible endpoint.
type Client struct {
	BaseURL  string
	APIKey   string
	Model    string
	Thinking string // "enabled", "disabled", "low", "medium", "high", or empty
	http     *http.Client
}

// maxResponseSize limits the LLM response body read to prevent DoS/OOM.
const maxResponseSize = 50 * 1024 * 1024 // 50 MB

// New creates a Client with the given timeout. Pass 0 to use the default
// (120s). The timeout applies per HTTP request — the agent loop may have
// multiple requests; set a generous timeout for deep-reasoning models.
func New(baseURL, apiKey, model, thinking string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		APIKey:   apiKey,
		Model:    model,
		Thinking: thinking,
		http:     &http.Client{Timeout: timeout},
	}
}

// Message represents a chat message.
type Message struct {
	Role       string     `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`                // text content
	Name       string     `json:"name,omitempty"`         // tool name (for tool role)
	ToolCallID string     `json:"tool_call_id,omitempty"` // required for tool role
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // required for assistant role with tool calls
}

// ToolCall represents a single tool invocation requested by the model.
// Matches the OpenAI API format exactly.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolDef is the JSON Schema definition of a tool.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a single tool's function signature.
type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// CallParams is the request body for /chat/completions.
type CallParams struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Tools           []ToolDef       `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	Thinking        *ThinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

// ThinkingConfig controls Deepseek's extended thinking feature.
type ThinkingConfig struct {
	Type string `json:"type"` // "enabled" or "disabled"
}

// CallResult is the parsed response from /chat/completions.
type CallResult struct {
	Content      string     // assistant text
	ToolCalls    []ToolCall // tool calls requested by the model
	InputTokens  int        // prompt_tokens from API usage (0 = not reported)
	OutputTokens int        // completion_tokens from API usage (0 = not reported)
}

// toolChoiceNone forces the model to not call tools.
var toolChoiceNone = "none"

// SimpleCall sends a single-turn chat completion request and returns the
// text response. No tools, no streaming, no thinking config. Used for
// lightweight LLM calls like skill risk assessment.
func (c *Client) SimpleCall(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	body := CallParams{
		Model:    c.Model,
		Messages: messages,
		Stream:   false,
	}

	reqBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return "", fmt.Errorf("llm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}
	if len(respBytes) > maxResponseSize {
		return "", fmt.Errorf("llm: response exceeds maximum size (%d bytes)", maxResponseSize)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: %s (status %d)", resp.Status, resp.StatusCode)
	}

	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &raw); err != nil {
		return "", fmt.Errorf("llm: parse response: %w", err)
	}
	if len(raw.Choices) == 0 {
		return "", fmt.Errorf("llm: empty response")
	}
	return raw.Choices[0].Message.Content, nil
}

// Call sends a chat completion request and returns the result.
func (c *Client) Call(ctx context.Context, messages []Message, tools []ToolDef) (*CallResult, error) {
	body := CallParams{
		Model:    c.Model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
	}

	switch c.Thinking {
	case "enabled", "disabled":
		body.Thinking = &ThinkingConfig{Type: c.Thinking}
	case "low", "medium", "high":
		body.ReasoningEffort = c.Thinking
	}

	reqBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("llm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("llm: read response: %w", err)
	}
	if len(respBytes) > maxResponseSize {
		return nil, fmt.Errorf("llm: response exceeds maximum size (%d bytes)", maxResponseSize)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: %s (status %d)", resp.Status, resp.StatusCode)
	}

	return parseResponse(respBytes)
}

func parseResponse(data []byte) (*CallResult, error) {
	var raw struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("llm: parse response: %w", err)
	}
	if len(raw.Choices) == 0 {
		return nil, fmt.Errorf("llm: no choices in response")
	}

	msg := raw.Choices[0].Message
	result := &CallResult{
		Content: msg.Content,
	}
	if raw.Usage != nil {
		result.InputTokens = raw.Usage.PromptTokens
		result.OutputTokens = raw.Usage.CompletionTokens
	}
	for _, tc := range msg.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return result, nil
}
