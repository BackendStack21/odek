package session

import (
	"encoding/json"
	"strings"
)

// Message is the persistable conversation record. The JSON shape is the v1
// OpenAI-compatible form (nested tool_calls[].function) so existing
// ~/.odek/sessions files load without a rewrite. ThinkingSignature is
// additive and omitted when empty.
//
// The SDK's Message is used only at the HTTP call boundary
// (internal/llmclient). Do not persist SDK types — they have no json tags.
type Message struct {
	Role              string        `json:"role"`
	Content           string        `json:"content"`
	Name              string        `json:"name,omitempty"`
	ToolCallID        string        `json:"tool_call_id,omitempty"`
	ToolCalls         []ToolCall    `json:"tool_calls,omitempty"`
	ReasoningContent  string        `json:"reasoning_content,omitempty"`
	ThinkingSignature string        `json:"thinking_signature,omitempty"`
	CacheControl      *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl is a leftover Anthropic marker from v1 transcripts. It is
// stored if present but never sent on OpenAI-format providers.
type CacheControl struct {
	Type string `json:"type"`
}

// ToolCall is a persistable tool invocation (OpenAI nested function shape).
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// UnmarshalJSON accepts v1 nested tool_calls and a flat v2 shape
// ({id,name,arguments}) so a future writer cannot break Load.
func (t *ToolCall) UnmarshalJSON(data []byte) error {
	var v1 struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Function  *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &v1); err != nil {
		return err
	}
	t.ID = v1.ID
	t.Type = v1.Type
	if t.Type == "" {
		t.Type = "function"
	}
	if v1.Function != nil {
		t.Function.Name = v1.Function.Name
		t.Function.Arguments = v1.Function.Arguments
		return nil
	}
	t.Function.Name = v1.Name
	t.Function.Arguments = v1.Arguments
	return nil
}

// ToolName returns the function name for a tool-result row. v1 stored it
// in Name; Gemini (and the SDK) need ToolName at the call boundary.
func (m Message) ToolName() string {
	if m.Name != "" {
		return m.Name
	}
	return ""
}

// UnknownRole reports whether the role is outside the canonical set.
func UnknownRole(role string) bool {
	switch strings.ToLower(role) {
	case "system", "user", "assistant", "tool":
		return false
	default:
		return true
	}
}
