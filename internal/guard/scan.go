package guard

import (
	"context"
	"fmt"
	"log"
)

// ScanContent checks content for prompt-injection threats.
//
// It always runs the fast, local rule-based scan first. If g is non-nil and the
// configured provider is not "local", it also runs a semantic second opinion via
// g. When the second opinion fails and FallbackToLocal is true, the content is
// accepted based on the local scan and a warning is logged.
//
// This function is the single source of truth for injection scanning across
// memory, system prompt sources, MCP descriptions, and any other guarded input.
func ScanContent(ctx context.Context, content string, g Guard, cfg *Config) error {
	if content == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// 1. Fast, local, zero-dependency scan.
	localRes, err := localGuardInstance.Detect(ctx, content)
	if err != nil {
		return err
	}
	if localRes.Injected {
		return fmt.Errorf("content contains %s", localRes.Label)
	}

	// 2. Semantic second opinion from the configured guard.
	if shouldRunGuard(g, cfg) {
		res, err := g.Detect(ctx, content)
		if err != nil {
			if isFallbackEnabled(cfg) {
				log.Printf("guard: sidecar call failed, accepting local scan: %v", err)
				return nil
			}
			return fmt.Errorf("sidecar unavailable: %w", err)
		}
		if res.Injected {
			return fmt.Errorf("injection detected (%s, score %.3f)", res.Label, res.Score)
		}
	}
	return nil
}

// isFallbackEnabled reports whether fallback to the local scan is enabled.
// It defaults to true when unset.
func isFallbackEnabled(cfg *Config) bool {
	if cfg == nil || cfg.FallbackToLocal == nil {
		return true
	}
	return *cfg.FallbackToLocal
}

// shouldRunGuard reports whether the configured guard should be consulted as a
// second opinion. The local guard is already covered by step 1, so we skip it.
func shouldRunGuard(g Guard, cfg *Config) bool {
	if g == nil {
		return false
	}
	if cfg != nil && cfg.Provider == ProviderLocal {
		return false
	}
	return true
}

// ScanContentWithScope runs ScanContent only when the scope is enabled in cfg.
// If the scope is disabled, only the local rule-based scan is run. This lets
// operators opt out of the semantic second opinion for specific surfaces without
// losing the fast local defense.
func ScanContentWithScope(ctx context.Context, content string, g Guard, cfg *Config, scope string) error {
	if !IsEnabled(cfg.Scan, scope) {
		return ScanContent(ctx, content, nil, nil)
	}
	return ScanContent(ctx, content, g, cfg)
}
