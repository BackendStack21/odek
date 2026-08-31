package config

import "time"

// LLMConfig tunes the shared LLM client (internal/llm). Nil section = the
// built-in defaults.
type LLMConfig struct {
	// StreamIdleTimeoutSeconds caps the time between SSE events (keepalive
	// comment lines count) before the stream is dropped and retried.
	// Thinking models can legitimately spend minutes before their first
	// event, so the built-in default is generous (120s). 0 keeps the
	// default. Config: llm.stream_idle_timeout_seconds,
	// ODEK_STREAM_IDLE_TIMEOUT_SECONDS.
	StreamIdleTimeoutSeconds int `json:"stream_idle_timeout_seconds,omitempty"`
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
