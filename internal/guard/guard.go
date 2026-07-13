// Package guard provides a pluggable prompt-injection detector for odek.
//
// It exposes a Guard interface with two built-in implementations:
//   - local: a zero-dependency rule-based guard backed by danger.ScanInjection.
//   - piguard: a client for the go-prompt-injection-guard sidecar (HTTP or Unix
//     socket).
//
// The package is designed to be used as a defense-in-depth layer: the fast,
// local scan is always available, and the piguard sidecar provides a semantic
// second opinion when configured.
package guard

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Provider constants.
const (
	ProviderLocal   = "local"
	ProviderPiguard = "piguard"
)

// Config controls the injection guard.
//
// SECURITY: this config is operator-controlled. A malicious project must not
// be able to disable the local scan or redirect memory/system-prompt content to
// an attacker-controlled endpoint. Therefore the entire guard section must be
// rejected from project-level ./odek.json.
type Config struct {
	Provider        string      `json:"provider,omitempty"`        // "local" or "piguard"
	URL             string      `json:"url,omitempty"`             // e.g. http://127.0.0.1:8080/detect
	LongURL         string      `json:"long_url,omitempty"`        // e.g. http://127.0.0.1:8080/long
	BatchURL        string      `json:"batch_url,omitempty"`       // e.g. http://127.0.0.1:8080/raw
	SocketPath      string      `json:"socket_path,omitempty"`     // /tmp/piguard.sock (unix mode)
	Threshold       float64     `json:"threshold,omitempty"`       // default 0.9
	TimeoutSeconds  int         `json:"timeout_seconds,omitempty"` // default 5
	FallbackToLocal *bool       `json:"fallback_to_local,omitempty"` // default true
	MaxTextLength   int         `json:"max_text_length,omitempty"` // default 0 = unlimited
	Scan            *ScanConfig `json:"scan,omitempty"`            // per-subsystem toggles
}

// ScanConfig toggles which subsystems use the guard.
type ScanConfig struct {
	Memory          *bool `json:"memory,omitempty"`           // default true
	SystemPrompt    *bool `json:"system_prompt,omitempty"`    // default true
	MCPDescriptions *bool `json:"mcp_descriptions,omitempty"` // default true
	Skills          *bool `json:"skills,omitempty"`           // default false
	ToolOutputs     *bool `json:"tool_outputs,omitempty"`     // default false
	Telegram        *bool `json:"telegram,omitempty"`           // default false
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// DefaultConfig returns a config that uses the local rule-based scan.
func DefaultConfig() *Config {
	return &Config{
		Provider:        ProviderLocal,
		Threshold:       0.9,
		TimeoutSeconds:  5,
		FallbackToLocal: boolPtr(true),
		Scan:            DefaultScanConfig(),
	}
}

// DefaultScanConfig enables memory, system_prompt, and mcp_descriptions by default.
func DefaultScanConfig() *ScanConfig {
	t := true
	f := false
	return &ScanConfig{
		Memory:          &t,
		SystemPrompt:    &t,
		MCPDescriptions: &t,
		Skills:          &f,
		ToolOutputs:     &f,
		Telegram:        &f,
	}
}

// Result is the outcome of a guard check.
type Result struct {
	Label    string
	Score    float64
	Injected bool
	Latency  time.Duration
}

// Guard is the pluggable prompt-injection detector.
type Guard interface {
	Detect(ctx context.Context, text string) (Result, error)
	DetectBatch(ctx context.Context, texts []string) ([]Result, error)
	DetectLong(ctx context.Context, text string) (Result, error)
	Close() error
}

// New creates a Guard from cfg.
// If cfg is nil, it returns a local guard.
// If the provider is unknown, it logs a warning and falls back to local.
// If the provider is piguard but initialization fails, it falls back to local
// when FallbackToLocal is true; otherwise it returns an error.
func New(cfg *Config) (Guard, error) {
	if cfg == nil {
		return NewLocalGuard(), nil
	}
	provider := cfg.Provider
	if provider == "" {
		provider = ProviderLocal
	}
	switch provider {
	case ProviderLocal:
		return NewLocalGuard(), nil
	case ProviderPiguard:
		g, err := newPiguardClient(cfg)
		if err != nil {
			return fallback(cfg, err)
		}
		return g, nil
	default:
		log.Printf("guard: unknown provider %q; falling back to local guard", provider)
		return NewLocalGuard(), nil
	}
}

// IsEnabled reports whether a scan scope is enabled in cfg.
// It returns true if the scope is nil or explicitly true, and false only when
// explicitly false. This makes "not set" mean "enabled by default" for the
// core surfaces, while preserving the ability to opt out.
func IsEnabled(cfg *ScanConfig, scope string) bool {
	if cfg == nil {
		return true
	}
	var ptr *bool
	switch scope {
	case "memory":
		ptr = cfg.Memory
	case "system_prompt":
		ptr = cfg.SystemPrompt
	case "mcp_descriptions":
		ptr = cfg.MCPDescriptions
	case "skills":
		ptr = cfg.Skills
	case "tool_outputs":
		ptr = cfg.ToolOutputs
	case "telegram":
		ptr = cfg.Telegram
	default:
		return true
	}
	if ptr == nil {
		return true
	}
	return *ptr
}

// timeout returns the configured timeout as a duration, with a safe default.
func timeout(cfg *Config) time.Duration {
	if cfg == nil || cfg.TimeoutSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(cfg.TimeoutSeconds) * time.Second
}

// threshold returns the configured threshold, with a safe default.
func threshold(cfg *Config) float64 {
	if cfg == nil || cfg.Threshold <= 0 {
		return 0.9
	}
	return cfg.Threshold
}

// truncate returns the input truncated to the configured MaxTextLength, or
// the original input if no limit is set. It preserves the original for the
// local scan; only the guard request body is truncated.
func truncateForGuard(text string, cfg *Config) string {
	if cfg == nil || cfg.MaxTextLength <= 0 {
		return text
	}
	if len(text) <= cfg.MaxTextLength {
		return text
	}
	return text[:cfg.MaxTextLength]
}

// fallback returns a local guard when cfg.FallbackToLocal is true, otherwise
// it returns the original error wrapped with msg.
func fallback(cfg *Config, err error) (Guard, error) {
	if cfg != nil && cfg.FallbackToLocal != nil && *cfg.FallbackToLocal {
		log.Printf("guard: sidecar unavailable, falling back to local guard: %v", err)
		return NewLocalGuard(), nil
	}
	return nil, fmt.Errorf("guard: sidecar unavailable: %w", err)
}

