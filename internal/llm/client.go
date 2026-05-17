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
	BaseURL string
	APIKey  string
	Model   string
	http    *http.Client
}

// New creates a Client with sensible defaults.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Message represents a chat message.
type Message struct {
	Role       string     `json:"role"`                         // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`                      // text content
	Name       string     `json:"name,omitempty"`               // tool name (for tool role)
	ToolCallID string     `json:"tool_call_id,omitempty"`       // required for tool role
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`         // required for assistant role with tool calls
}

// ToolCall represents a single tool invocation requested by the model.
// Matches the OpenAI API format exactly.
type ToolCall struct {
	ID   string `json:"id"`
	Type string `json:"type"` // always "function"
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
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

// CallResult is the parsed response from /chat/completions.
type CallResult struct {
	Content   string     // assistant text
	ToolCalls []ToolCall // tool calls requested by the model
}

// toolChoiceNone forces the model to not call tools.
var toolChoiceNone = "none"

// Call sends a chat completion request and returns the result.
func (c *Client) Call(ctx context.Context, messages []Message, tools []ToolDef) (*CallResult, error) {
	body := CallParams{
		Model:    c.Model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
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

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: %s (status %d): %s", resp.Status, resp.StatusCode, string(respBytes))
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
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("llm: parse response: %w (body: %s)", err, string(data))
	}
	if len(raw.Choices) == 0 {
		return nil, fmt.Errorf("llm: no choices in response")
	}

	msg := raw.Choices[0].Message
	result := &CallResult{
		Content: msg.Content,
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
