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
	BaseURL    string
	APIKey     string
	Model      string
	Thinking   string // "enabled", "disabled", "low", "medium", "high", or empty
	MaxTokens  int    // max output tokens (0 = provider default)
	http       *http.Client
}

// maxResponseSize limits the LLM response body read to prevent DoS/OOM.
const maxResponseSize = 50 * 1024 * 1024 // 50 MB

// New creates a Client with the given timeout. Pass 0 to use the default
// (120s). The timeout applies per HTTP request — the agent loop may have
// multiple requests; set a generous timeout for deep-reasoning models.
func New(baseURL, apiKey, model, thinking string, timeout time.Duration) *Client {
	return NewWithMaxTokens(baseURL, apiKey, model, thinking, 0, timeout)
}

// NewWithMaxTokens creates a Client with a specific max_tokens setting.
// maxTokens=0 means no limit (provider default).
func NewWithMaxTokens(baseURL, apiKey, model, thinking string, maxTokens int, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    apiKey,
		Model:     model,
		Thinking:  thinking,
		MaxTokens: maxTokens,
		http:      &http.Client{Timeout: timeout},
	}
}

// CacheControl marks a message or system block as cacheable by Anthropic.
// Providers that don't support it (OpenAI, DeepSeek) silently ignore the field.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// SystemBlock represents an Anthropic-style system prompt block with optional
// cache control. OpenAI-compatible endpoints that don't support this format
// silently ignore the field.
type SystemBlock struct {
	Type         string       `json:"type"` // "text"
	Text         string       `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Message represents a chat message.
type Message struct {
	Role        string       `json:"role"`                  // "system", "user", "assistant", "tool"
	Content     string       `json:"content"`               // text content
	Name        string       `json:"name,omitempty"`        // tool name (for tool role)
	ToolCallID  string       `json:"tool_call_id,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`  // required for assistant role with tool calls
	CacheControl *CacheControl `json:"cache_control,omitempty"` // Anthropic prompt caching marker
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
	System          []SystemBlock   `json:"system,omitempty"`         // Anthropic-style system blocks
	Tools           []ToolDef       `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	MaxTokens       int             `json:"max_tokens,omitempty"`     // max output tokens (0 = omit/provider default)
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

	// Cache metrics. Only populated when the provider returns them.
	// Anthropic: cache_creation_input_tokens, cache_read_input_tokens
	// OpenAI: prompt_tokens_details.cached_tokens
	CacheCreationTokens int // Anthropic — tokens written to cache
	CacheReadTokens     int // Anthropic — tokens read from cache hit
	CachedTokens        int // OpenAI — cached tokens in prompt
}

// toolChoiceNone forces the model to not call tools.
var toolChoiceNone = "none"

// ApplyCacheMarkers annotates messages with Anthropic-style cache_control
// markers to enable prompt caching. It:
//  1. Marks the first system message (if present) with cache_control: ephemeral
//  2. Marks the first user message with cache_control: ephemeral
//
// Returns the updated messages and a System field (populated if the system
// message was moved out of the messages array for Anthropic compatibility).
// Providers that don't support prompt caching silently ignore these fields.
func ApplyCacheMarkers(messages []Message) ([]Message, []SystemBlock) {
	var systemBlocks []SystemBlock
	annotated := make([]Message, 0, len(messages))

	// Track whether we've marked the first user message
	markedUser := false

	for i, m := range messages {
		// If this is the first system message, move it to System field
		// (Anthropic format) with cache_control
		if m.Role == "system" && len(systemBlocks) == 0 {
			systemBlocks = append(systemBlocks, SystemBlock{
				Type: "text",
				Text: m.Content,
				CacheControl: &CacheControl{Type: "ephemeral"},
			})
			continue // don't add to messages — it's now in System
		}

		// Mark the first user message with cache_control
		if m.Role == "user" && !markedUser {
			m.CacheControl = &CacheControl{Type: "ephemeral"}
			markedUser = true
		}

		// For assistant messages with preceded by system (now in System field),
		// mark them too if they're the first non-system assistant response
		// (This helps cache the initial turn in multi-turn conversations)
		if i > 0 && m.Role == "assistant" && len(m.ToolCalls) == 0 && !markedUser {
			// This is the final assistant message from a previous run —
			// keep going, we already marked the first user above
		}

		annotated = append(annotated, m)
	}

	return annotated, systemBlocks
}

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
// systemBlocks is optional — pass nil for providers that don't support
// the separate System field (OpenAI, DeepSeek). When non-nil, the system
// prompt is sent in the "system" field instead of as a system message in
// the messages array (Anthropic format for prompt caching).
func (c *Client) Call(ctx context.Context, messages []Message, systemBlocks []SystemBlock, tools []ToolDef) (*CallResult, error) {
	body := CallParams{
		Model:     c.Model,
		Messages:  messages,
		System:    systemBlocks,
		Tools:     tools,
		Stream:    false,
		MaxTokens: c.MaxTokens,
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

	// Add Anthropic-specific API version header (required for prompt caching)
	// Safe to always send — other providers ignore unknown headers.
	req.Header.Set("anthropic-version", "2023-06-01")

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
			// Anthropic prompt caching
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			// OpenAI prompt caching (nested details)
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
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
		result.CacheCreationTokens = raw.Usage.CacheCreationTokens
		result.CacheReadTokens = raw.Usage.CacheReadTokens
		if raw.Usage.PromptTokensDetails != nil {
			result.CachedTokens = raw.Usage.PromptTokensDetails.CachedTokens
		}
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
