// Package llm provides an OpenAI-compatible HTTP client using only stdlib.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/transport"
)

// Client sends chat completion requests to any OpenAI-compatible endpoint.
type Client struct {
	BaseURL        string
	APIKey         string
	Model          string
	Thinking       string  // "enabled", "disabled", "low", "medium", "high", or empty
	ThinkingBudget int     // max thinking tokens for Anthropic extended thinking (0 = use default 5000)
	MaxTokens      int     // max output tokens (0 = provider default)
	Temperature    float64 // 0 = use provider default, <0 = omit from request
	http           *http.Client
}

// maxResponseSize limits the LLM response body read to prevent DoS/OOM.
const maxResponseSize = 50 * 1024 * 1024 // 50 MB

// New creates a Client with the given timeout. Pass 0 to use the default
// (120s). The timeout applies per HTTP request — the agent loop may have
// multiple requests; set a generous timeout for deep-reasoning models.
func New(baseURL, apiKey, model, thinking string, thinkingBudget int, timeout time.Duration) *Client {
	return NewWithMaxTokens(baseURL, apiKey, model, thinking, thinkingBudget, 0, timeout)
}

// NewWithMaxTokens creates a Client with a specific max_tokens setting.
// maxTokens=0 means no limit (provider default).
func NewWithMaxTokens(baseURL, apiKey, model, thinking string, thinkingBudget int, maxTokens int, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Client{
		BaseURL:        strings.TrimRight(baseURL, "/"),
		APIKey:         apiKey,
		Model:          model,
		Thinking:       thinking,
		ThinkingBudget: thinkingBudget,
		MaxTokens:      maxTokens,
		http:           transport.NewPooledClient(timeout),
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
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Message represents a chat message.
type Message struct {
	Role             string        `json:"role"`           // "system", "user", "assistant", "tool"
	Content          string        `json:"content"`        // text content
	Name             string        `json:"name,omitempty"` // tool name (for tool role)
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`        // required for assistant role with tool calls
	ReasoningContent string        `json:"reasoning_content,omitempty"` // DeepSeek reasoning tokens, must be echoed back
	CacheControl     *CacheControl `json:"cache_control,omitempty"`     // Anthropic prompt caching marker
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
	System          []SystemBlock   `json:"system,omitempty"` // Anthropic-style system blocks
	Tools           []ToolDef       `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	MaxTokens       int             `json:"max_tokens,omitempty"`  // max output tokens (0 = omit/provider default)
	Temperature     *float64        `json:"temperature,omitempty"` // 0–2, nil = provider default
	Thinking        *ThinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

// ThinkingConfig controls extended thinking for DeepSeek and Anthropic models.
// Anthropic requires budget_tokens when type is "enabled"; DeepSeek ignores it.
type ThinkingConfig struct {
	Type         string `json:"type"`                    // "enabled" or "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // Anthropic: max thinking tokens
}

// CallResult is the parsed response from /chat/completions.
type CallResult struct {
	Content          string     // assistant text
	ReasoningContent string     // DeepSeek reasoning/thinking tokens
	ToolCalls        []ToolCall // tool calls requested by the model
	InputTokens      int        // prompt_tokens from API usage (0 = not reported)
	OutputTokens     int        // completion_tokens from API usage (0 = not reported)

	// Cache metrics. Only populated when the provider returns them.
	// Anthropic: cache_creation_input_tokens, cache_read_input_tokens
	// OpenAI: prompt_tokens_details.cached_tokens
	// DeepSeek: prompt_cache_hit_tokens (read), prompt_cache_miss_tokens (write)
	CacheCreationTokens int // Anthropic — tokens written to cache
	CacheReadTokens     int // Anthropic — tokens read from cache hit
	CachedTokens        int // OpenAI — cached tokens in prompt
	// CacheReported is true when the provider returned any cache metrics at
	// all; false means "no data", which is different from "0 tokens cached".
	CacheReported bool
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
				Type:         "text",
				Text:         m.Content,
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

	// Share the main loop's retry/backoff so a transient blip doesn't abort
	// these best-effort secondary calls (skill matching, memory summaries,
	// episode extraction, session titles).
	respBytes, err := c.postChatWithRetry(ctx, reqBytes)
	if err != nil {
		return "", err
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
	case "enabled":
		// Anthropic requires budget_tokens when enabling thinking.
		// 5000 is a safe default: leaves ample room for the text response
		// even on models with 8K max output (e.g. Claude Haiku).
		// DeepSeek silently ignores the field.
		budget := c.ThinkingBudget
		if budget <= 0 {
			budget = 5000
		}
		body.Thinking = &ThinkingConfig{Type: "enabled", BudgetTokens: budget}
		// Anthropic also requires temperature=1 when thinking is enabled.
		// Force it regardless of the configured temperature to avoid a 400.
		one := float64(1)
		body.Temperature = &one
	case "disabled":
		body.Thinking = &ThinkingConfig{Type: "disabled"}
		if c.Temperature >= 0 {
			body.Temperature = &c.Temperature
		}
	default:
		if c.Temperature >= 0 {
			body.Temperature = &c.Temperature
		}
		if c.Thinking == "low" || c.Thinking == "medium" || c.Thinking == "high" {
			body.ReasoningEffort = c.Thinking
		}
	}

	reqBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	respBytes, err := c.postChatWithRetry(ctx, reqBytes)
	if err != nil {
		return nil, err
	}
	return parseResponse(respBytes)
}

// postChatWithRetry POSTs reqBytes to /chat/completions and returns the raw 200
// response body, retrying transient network errors and retryable HTTP statuses
// (429, 502, 503, 504) with exponential backoff. Shared by every chat call so
// the main loop and the lightweight secondary calls (SimpleCall) get identical
// resilience. Respects ctx cancellation during the backoff sleep.
func (c *Client) postChatWithRetry(ctx context.Context, reqBytes []byte) ([]byte, error) {
	url := c.BaseURL + "/chat/completions"

	const maxRetries = 3
	var lastErr error
	var wait time.Duration // how long to sleep before the next attempt

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		// Default backoff for the next attempt if this one fails: 1s, 2s, 4s.
		// A Retry-After header on a 429/503 overrides it below.
		wait = time.Duration(1<<attempt) * time.Second

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
		if err != nil {
			return nil, fmt.Errorf("llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm: %w", err)
			if isRetryableNetworkError(err) {
				continue
			}
			return nil, lastErr
		}

		respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("llm: read response: %w", err)
			continue
		}
		if len(respBytes) > maxResponseSize {
			return nil, fmt.Errorf("llm: response exceeds maximum size (%d bytes)", maxResponseSize)
		}

		if resp.StatusCode != http.StatusOK {
			errBody := strings.TrimSpace(string(respBytes))
			if errBody != "" {
				lastErr = fmt.Errorf("llm: %s (status %d): %s", resp.Status, resp.StatusCode, errBody)
			} else {
				lastErr = fmt.Errorf("llm: %s (status %d)", resp.Status, resp.StatusCode)
			}
			if isRetryableHTTPStatus(resp.StatusCode) {
				// Honor the server's Retry-After (seconds or HTTP-date) when it
				// asks us to wait longer than our default backoff — otherwise a
				// rate-limited turn burns all three retries in ~7s and fails
				// even though the server told us exactly when to come back.
				if ra := parseRetryAfter(retryAfter); ra > 0 {
					wait = ra
				}
				continue
			}
			return nil, lastErr
		}

		return respBytes, nil
	}

	return nil, fmt.Errorf("llm: retry exhausted (%d attempts): %w", maxRetries+1, lastErr)
}

// maxRetryAfter caps how long we'll honor a server's Retry-After. A pathological
// or hostile value (e.g. "Retry-After: 86400") must not wedge a turn for hours;
// ctx cancellation can still break the wait sooner.
const maxRetryAfter = 60 * time.Second

// parseRetryAfter interprets an HTTP Retry-After header, which is either an
// integer number of seconds or an HTTP-date. Returns 0 when absent or
// unparseable (callers then fall back to exponential backoff). The result is
// capped at maxRetryAfter.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
		if d <= 0 {
			return 0
		}
	} else {
		return 0
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

// isRetryableHTTPStatus returns true for HTTP status codes that indicate
// a transient error safe to retry after a backoff.
func isRetryableHTTPStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// isRetryableNetworkError returns true for network errors that are likely
// transient (connection refused, timeout, EOF before headers).
func isRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// Common transient network error patterns
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "TLS handshake timeout")
}

func parseResponse(data []byte) (*CallResult, error) {
	var raw struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
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
			// DeepSeek native prompt caching (always present on DeepSeek
			// endpoints, unlike prompt_tokens_details which varies by
			// gateway/proxy).
			PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
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
		Content:          msg.Content,
		ReasoningContent: msg.ReasoningContent,
	}
	if raw.Usage != nil {
		result.InputTokens = raw.Usage.PromptTokens
		result.OutputTokens = raw.Usage.CompletionTokens
		result.CacheCreationTokens = raw.Usage.CacheCreationTokens
		result.CacheReadTokens = raw.Usage.CacheReadTokens
		if raw.Usage.PromptTokensDetails != nil {
			result.CachedTokens = raw.Usage.PromptTokensDetails.CachedTokens
			result.CacheReported = true
		}
		if raw.Usage.CacheCreationTokens > 0 || raw.Usage.CacheReadTokens > 0 {
			result.CacheReported = true
		}
		// DeepSeek native fields: a hit is prompt content read from cache;
		// a miss is newly processed content that DeepSeek then caches for
		// future requests, i.e. a cache write.
		if raw.Usage.PromptCacheHitTokens > 0 || raw.Usage.PromptCacheMissTokens > 0 {
			result.CacheReadTokens += raw.Usage.PromptCacheHitTokens
			result.CacheCreationTokens += raw.Usage.PromptCacheMissTokens
			result.CacheReported = true
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
