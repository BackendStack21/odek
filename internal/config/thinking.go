package config

import "strings"

// Canonical thinking levels exposed to CLI, config, serve, and clients.
// go-llm-sdk maps these onto provider fields (reasoning_effort, thinking
// object, Anthropic/Gemini budgets). Aliases are accepted inbound only.
const (
	ThinkingDisabled = "disabled"
	ThinkingLow      = "low"
	ThinkingMedium   = "medium"
	ThinkingHigh     = "high"
)

// NormalizeThinking maps an inbound thinking value onto the public contract
// (disabled | low | medium | high) or the empty inherit/omit sentinel.
// ok is false when the value is non-empty and unrecognized.
func NormalizeThinking(s string) (canonical string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", true
	case ThinkingDisabled, ThinkingLow, ThinkingMedium, ThinkingHigh:
		return strings.ToLower(strings.TrimSpace(s)), true
	case "mid":
		return ThinkingMedium, true
	case "enabled", "on", "true", "1":
		return ThinkingMedium, true
	case "max":
		return ThinkingHigh, true
	case "off", "false", "0":
		return ThinkingDisabled, true
	default:
		return "", false
	}
}

// CanonicalThinking returns the public string for config views: a canonical
// level, or "" when unset / unrecognized. Unrecognized values are not
// echoed — clients must not treat garbage as a level.
func CanonicalThinking(s string) string {
	canon, ok := NormalizeThinking(s)
	if !ok {
		return ""
	}
	return canon
}
