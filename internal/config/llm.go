package config

import "time"

// LLMConfig tunes the shared go-llm-sdk client. Nil section = the built-in
// defaults (120s request + idle timeouts; context window from ListModels
// then the last-resort table).
type LLMConfig struct {
	// RequestTimeoutSeconds is the per-request wall-clock budget.
	// 0 keeps the SDK default (120s). Config: llm.request_timeout_seconds,
	// ODEK_REQUEST_TIMEOUT_SECONDS.
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`

	// StreamIdleTimeoutSeconds caps the time between SSE events (keepalive
	// comment lines count) before the stream is dropped and retried.
	// Thinking models can legitimately spend minutes before their first
	// event, so the built-in default is generous (120s). 0 keeps the
	// default. Config: llm.stream_idle_timeout_seconds,
	// ODEK_STREAM_IDLE_TIMEOUT_SECONDS.
	StreamIdleTimeoutSeconds int `json:"stream_idle_timeout_seconds,omitempty"`

	// ContextWindow overrides ListModels / last-resort discovery when > 0.
	// Config: llm.context_window, ODEK_CONTEXT_WINDOW.
	ContextWindow int `json:"context_window,omitempty"`
}

// llmStreamIdleTimeoutFrom merges the file value with the env override (env
// wins, per the priority chain) and clamps to a sane floor. Returns 0 when
// unset so the caller keeps the llm package default.
func llmStreamIdleTimeoutFrom(llm *LLMConfig, envSeconds *int) time.Duration {
	v := 0
	if llm != nil {
		v = llm.StreamIdleTimeoutSeconds
	}
	if envSeconds != nil && *envSeconds > 0 {
		v = *envSeconds
	}
	if v <= 0 {
		return 0
	}
	if v < 5 {
		v = 5
	}
	return time.Duration(v) * time.Second
}
