// Package llmclient adapts go-llm-sdk for odek. It is not an HTTP client:
// all wire, retry, and streaming logic lives in the SDK. This package
// owns odek's conversation DTO ↔ SDK request mapping, temperature polarity,
// and the SimpleCall helper used by memory/titles.
package llmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/session"
	"github.com/BackendStack21/odek/internal/transport"
)

// Re-export the SDK types the rest of odek still names in callbacks.
type (
	Delta              = sdk.Delta
	DeltaKind          = sdk.DeltaKind
	RateLimitError     = sdk.RateLimitError
	StreamAbortedError = sdk.StreamAbortedError
	APIError           = sdk.APIError
	ChatResult         = sdk.ChatResult
	ToolDef            = sdk.ToolDef
	SystemBlock        = sdk.SystemBlock
)

const (
	DeltaReasoning  = sdk.DeltaReasoning
	DeltaContent    = sdk.DeltaContent
	DeltaToolArgs   = sdk.DeltaToolArgs
	FormatOpenAI    = sdk.FormatOpenAI
	FormatAnthropic = sdk.FormatAnthropic
	FormatGemini    = sdk.FormatGemini
)

// Client is one bound provider+model chat client plus the odek-side
// request knobs (thinking, temperature polarity, prompt caching).
type Client struct {
	SDK      *sdk.SDK
	Chat     *sdk.ChatClient
	Provider *sdk.Provider

	Thinking       string
	ThinkingBudget int
	MaxTokens      int
	// Temperature uses odek polarity: 0 = send explicit 0; <0 = omit.
	Temperature float64
	PromptCache bool
}

// Options builds an SDK from resolved operator config.
type Options struct {
	Provider    string
	Model       string
	APIKey      string // selected-provider override
	BaseURL     string // selected-provider override
	Providers   map[string]ProviderOverride
	Timeout     time.Duration
	IdleTimeout time.Duration
}

// ProviderOverride is one providers.<id> entry.
type ProviderOverride struct {
	APIKey  string
	BaseURL string
	Format  string // openai | anthropic | gemini; required for custom ids
}

// NewSDK constructs a process-usable SDK from resolved options. Keys come
// from overrides, not a post-Unsetenv FromEnv(). Transport is odek's pooled
// dialer (HTTP_PROXY + one pool).
func NewSDK(opts Options) (*sdk.SDK, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	sdkOpts := []sdk.Option{
		sdk.WithRequestTimeout(timeout),
		sdk.WithTransport(transport.PooledTransport()),
	}
	for id, ov := range opts.Providers {
		popts := []sdk.ProviderOption{}
		if ov.APIKey != "" {
			popts = append(popts, sdk.WithAPIKey(ov.APIKey))
		}
		if ov.BaseURL != "" {
			popts = append(popts, sdk.WithBaseURL(ov.BaseURL))
		}
		if ov.Format != "" {
			popts = append(popts, sdk.WithFormat(sdk.Format(ov.Format)))
		}
		if len(popts) > 0 {
			sdkOpts = append(sdkOpts, sdk.WithProvider(id, popts...))
		}
	}
	id := opts.Provider
	if id == "" {
		id = "deepseek"
	}
	sel := []sdk.ProviderOption{}
	if opts.APIKey != "" {
		sel = append(sel, sdk.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		sel = append(sel, sdk.WithBaseURL(opts.BaseURL))
	}
	if len(sel) > 0 {
		sdkOpts = append(sdkOpts, sdk.WithProvider(id, sel...))
	}
	s := sdk.New(sdkOpts...)
	if opts.IdleTimeout > 0 {
		sdk.SetStreamIdleTimeout(opts.IdleTimeout)
	}
	return s, nil
}

// Dial builds a one-off SDK+Chat for a single provider (tests and side paths).
func Dial(providerID, model, apiKey, baseURL string) (*Client, error) {
	if providerID == "" {
		if id := InferProvider(baseURL); id != "" {
			providerID = id
			baseURL = CanonicalBaseURL(id, baseURL)
		} else {
			providerID = "legacy"
		}
	}
	ov := ProviderOverride{APIKey: apiKey, BaseURL: baseURL}
	if providerID == "legacy" {
		ov.Format = "openai"
	}
	s, err := NewSDK(Options{
		Provider:  providerID,
		Model:     model,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		Providers: map[string]ProviderOverride{providerID: ov},
	})
	if err != nil {
		return nil, err
	}
	return New(s, providerID, model)
}

// New binds a ChatClient for provider+model.
func New(s *sdk.SDK, providerID, model string) (*Client, error) {
	if providerID == "" {
		providerID = "deepseek"
	}
	p, err := s.Provider(providerID)
	if err != nil {
		return nil, err
	}
	chat, err := s.Chat(providerID, model)
	if err != nil {
		return nil, err
	}
	return &Client{SDK: s, Chat: chat, Provider: p}, nil
}

// RebindModel returns a Client for a different model on the same SDK/provider,
// copying thinking/temperature/cache knobs.
func (c *Client) RebindModel(model string) (*Client, error) {
	if c == nil || c.SDK == nil {
		return nil, fmt.Errorf("llm: no sdk")
	}
	n, err := New(c.SDK, c.ProviderID(), model)
	if err != nil {
		return nil, err
	}
	n.Thinking = c.Thinking
	n.ThinkingBudget = c.ThinkingBudget
	n.MaxTokens = c.MaxTokens
	n.Temperature = c.Temperature
	n.PromptCache = c.PromptCache
	return n, nil
}

// Model returns the bound model id.
func (c *Client) Model() string {
	if c == nil || c.Chat == nil {
		return ""
	}
	return c.Chat.Model()
}

// ProviderID returns the bound provider id.
func (c *Client) ProviderID() string {
	if c == nil || c.Provider == nil {
		return ""
	}
	return c.Provider.ID()
}

// RequestTimeout reports the per-request wall-clock budget.
func (c *Client) RequestTimeout() time.Duration {
	if c == nil || c.Chat == nil {
		return 0
	}
	return c.Chat.RequestTimeout()
}

// SetRequestTimeout adjusts the bound client's timeout. Do not call this
// on the main run client for memory-only overrides — mint a second Chat.
func (c *Client) SetRequestTimeout(d time.Duration) {
	if c != nil && c.Chat != nil {
		c.Chat.SetRequestTimeout(d)
	}
}

// Format returns the bound provider's wire format.
func (c *Client) Format() sdk.Format {
	if c == nil || c.Provider == nil {
		return sdk.FormatOpenAI
	}
	return c.Provider.Config().Format
}

// IsAnthropic reports FormatAnthropic (never URL sniffing).
func (c *Client) IsAnthropic() bool {
	return c.Format() == sdk.FormatAnthropic
}

// SimpleCall is the memory/title helper: one buffered turn, no tools.
func (c *Client) SimpleCall(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	res, err := c.Chat.Call(ctx, &sdk.ChatRequest{
		System:      []sdk.SystemBlock{{Text: systemPrompt}},
		Messages:    []sdk.Message{{Role: sdk.RoleUser, Content: userPrompt}},
		Thinking:    "disabled",
		Temperature: sdkTemperature(c.Temperature),
		MaxTokens:   c.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("llm: empty response")
	}
	return res.Content, nil
}

// CallResult is the loop-facing result. Cache fields come from SDK Usage
// when the gap-fix SDK is pinned.
type CallResult struct {
	Content             string
	ReasoningContent    string
	ThinkingSignature   string
	ToolCalls           []session.ToolCall
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	CachedTokens        int
	CacheReported       bool
	FinishReason        string
}

// Call runs a buffered completion.
func (c *Client) Call(ctx context.Context, messages []session.Message, tools []ToolDef) (*CallResult, error) {
	req := c.buildRequest(messages, tools)
	res, err := c.Chat.Call(ctx, req)
	if err != nil {
		return mapResult(res), err
	}
	return mapResult(res), nil
}

// CallStream runs a streaming completion.
func (c *Client) CallStream(ctx context.Context, messages []session.Message, tools []ToolDef, cb func(Delta) error) (*CallResult, error) {
	if cb == nil {
		return c.Call(ctx, messages, tools)
	}
	req := c.buildRequest(messages, tools)
	res, err := c.Chat.CallStream(ctx, req, cb)
	if err != nil {
		return mapResult(res), err
	}
	return mapResult(res), nil
}

func (c *Client) buildRequest(messages []session.Message, tools []ToolDef) *sdk.ChatRequest {
	sys, msgs := toSDKMessages(messages, c.PromptCache && c.IsAnthropic())
	return &sdk.ChatRequest{
		System:         sys,
		Messages:       msgs,
		Tools:          tools,
		Thinking:       c.Thinking,
		ThinkingBudget: c.ThinkingBudget,
		MaxTokens:      c.MaxTokens,
		Temperature:    sdkTemperature(c.Temperature),
	}
}

// sdkTemperature maps odek polarity onto the SDK:
//
//	odek 0  (default, send explicit 0) → SDK -1
//	odek <0 (omit)                     → SDK 0
//	odek >0                            → same
func sdkTemperature(t float64) float64 {
	if t == 0 {
		return -1
	}
	if t < 0 {
		return 0
	}
	return t
}

func toSDKMessages(in []session.Message, cacheAnthropic bool) ([]sdk.SystemBlock, []sdk.Message) {
	var sys []sdk.SystemBlock
	out := make([]sdk.Message, 0, len(in))
	// Convert-at-call: drop an unknown-role row together with its
	// assistant+tool group (same pairing rule as session trim).
	skip := make(map[int]bool)
	for i, m := range in {
		if session.UnknownRole(m.Role) {
			start, end := groupBounds(in, i)
			for j := start; j < end; j++ {
				skip[j] = true
			}
		}
	}
	for i, m := range in {
		if skip[i] {
			continue
		}
		switch m.Role {
		case "system":
			sb := sdk.SystemBlock{Text: m.Content}
			if cacheAnthropic && len(sys) == 0 {
				sb.Cache = true
			}
			sys = append(sys, sb)
		case "user":
			out = append(out, sdk.Message{
				Role:    sdk.RoleUser,
				Content: m.Content,
				Cache:   cacheAnthropic && !hasUser(out),
			})
		case "assistant":
			out = append(out, sdk.Message{
				Role:              sdk.RoleAssistant,
				Content:           m.Content,
				ReasoningContent:  m.ReasoningContent,
				ThinkingSignature: m.ThinkingSignature,
				ToolCalls:         toSDKToolCalls(m.ToolCalls),
			})
		case "tool":
			out = append(out, sdk.Message{
				Role:       sdk.RoleTool,
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
				ToolName:   m.ToolName(),
			})
		}
	}
	return sys, out
}

func hasUser(msgs []sdk.Message) bool {
	for _, m := range msgs {
		if m.Role == sdk.RoleUser {
			return true
		}
	}
	return false
}

func groupBounds(in []session.Message, i int) (start, end int) {
	// Walk back to the assistant tool_calls parent if this row is a tool
	// result or an unknown sibling after one.
	start = i
	for start > 0 && in[start].Role == "tool" {
		start--
	}
	if start > 0 && in[start].Role != "assistant" {
		// unknown role in the middle of a tool group: include the parent
		for j := start; j >= 0; j-- {
			if in[j].Role == "assistant" && len(in[j].ToolCalls) > 0 {
				start = j
				break
			}
		}
	}
	end = i + 1
	if start < len(in) && in[start].Role == "assistant" && len(in[start].ToolCalls) > 0 {
		end = start + 1
		for end < len(in) && (in[end].Role == "tool" || session.UnknownRole(in[end].Role)) {
			end++
		}
	}
	return start, end
}

func toSDKToolCalls(in []session.ToolCall) []sdk.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]sdk.ToolCall, len(in))
	for i, tc := range in {
		out[i] = sdk.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
	}
	return out
}

func mapResult(res *sdk.ChatResult) *CallResult {
	if res == nil {
		return nil
	}
	out := &CallResult{
		Content:             res.Content,
		ReasoningContent:    res.ReasoningContent,
		ThinkingSignature:   res.ThinkingSignature,
		FinishReason:        res.FinishReason,
		InputTokens:         res.Usage.PromptTokens,
		OutputTokens:        res.Usage.CompletionTokens,
		CacheCreationTokens: res.Usage.CacheCreationTokens,
		CacheReadTokens:     res.Usage.CacheReadTokens,
		CachedTokens:        res.Usage.CachedTokens,
		CacheReported:       res.Usage.CacheReported,
	}
	for _, tc := range res.ToolCalls {
		var st session.ToolCall
		st.ID = tc.ID
		st.Type = "function"
		st.Function.Name = tc.Name
		st.Function.Arguments = tc.Arguments
		out.ToolCalls = append(out.ToolCalls, st)
	}
	return out
}

// ToolsFromSchema converts registry tools to SDK ToolDefs.
func ToolsFromSchema(name, desc string, schema any) (ToolDef, error) {
	var params json.RawMessage
	switch s := schema.(type) {
	case json.RawMessage:
		params = s
	case []byte:
		params = s
	case string:
		if strings.TrimSpace(s) != "" {
			params, _ = json.Marshal(map[string]any{"type": "object", "raw_schema": s})
		} else {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
	default:
		b, err := json.Marshal(schema)
		if err != nil {
			return ToolDef{}, err
		}
		params = b
	}
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return ToolDef{Name: name, Description: desc, Parameters: params}, nil
}

// InferProvider maps a v1 base URL host onto a built-in SDK id.
// Official Anthropic/Gemini path suffixes are stripped by the caller
// before this is used as a WithBaseURL value.
func InferProvider(baseURL string) string {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, "api.anthropic.com"):
		return "anthropic"
	case strings.Contains(u, "generativelanguage.googleapis.com"):
		return "gemini"
	case strings.Contains(u, "api.openai.com"):
		return "openai"
	case strings.Contains(u, "api.deepseek.com"):
		return "deepseek"
	case strings.Contains(u, "api.z.ai") || strings.Contains(u, "bigmodel.cn"):
		return "zai"
	case strings.Contains(u, "api.moonshot.ai"):
		return "kimi"
	default:
		return ""
	}
}

// CanonicalBaseURL returns the URL to store on a built-in provider override.
// Anthropic/Gemini SDK clients join /v1/messages and /v1beta/... themselves,
// so a v1 config of https://api.anthropic.com/v1 must not be copied as-is.
func CanonicalBaseURL(providerID, baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	switch providerID {
	case "anthropic":
		trimmed = strings.TrimSuffix(trimmed, "/v1")
		if trimmed == "" || trimmed == "https://api.anthropic.com" {
			return "" // use SDK default
		}
	case "gemini":
		trimmed = strings.TrimSuffix(trimmed, "/v1beta")
		if trimmed == "" || trimmed == "https://generativelanguage.googleapis.com" {
			return ""
		}
	}
	return trimmed
}

// LastResortContext is the safety-net window when ListModels reports 0.
// No thinking/timeout defaults live here.
func LastResortContext(model string) int {
	type entry struct {
		prefix string
		ctx    int
	}
	// Longest prefix wins — same order as the old KnownProfiles table.
	table := []entry{
		{"glm-5.3", 1_000_000},
		{"glm-5.2", 1_000_000},
		{"glm-5-turbo", 200_000},
		{"glm-", 131_072},
		{"kimi-", 262_144},
		{"k3-256k", 262_144},
		{"k3", 1_000_000},
		{"deepseek-v4-pro", 1_000_000},
		{"deepseek-v4-flash", 131_072},
		{"deepseek-", 131_072},
	}
	best, bestLen := 0, 0
	for _, e := range table {
		if strings.HasPrefix(model, e.prefix) && len(e.prefix) > bestLen {
			best, bestLen = e.ctx, len(e.prefix)
		}
	}
	return best
}

// DiscoverContext asks ListModels for the model's window (5s bound).
func DiscoverContext(ctx context.Context, p *sdk.Provider, model string) int {
	if p == nil || model == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	models, err := p.ListModels(ctx)
	if err != nil {
		return 0
	}
	for _, m := range models {
		if m.ID == model && m.ContextWindow > 0 {
			return m.ContextWindow
		}
	}
	return 0
}
