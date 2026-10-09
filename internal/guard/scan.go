package guard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"unicode/utf8"
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
		res, err := sidecarDetect(ctx, g, content, cfg)
		if errors.Is(err, errSidecarOversize) {
			// Fail closed: padding a document past the window cap must not
			// turn the sidecar check into a fallback acceptance.
			return err
		}
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

// sidecarBatchSize bounds how many windows travel in one batch round trip.
const sidecarBatchSize = 16

// maxSidecarWindows bounds the round trips one scan may cost. Content that
// would need more windows is rejected outright, whatever fallback_to_local
// says: accepting it on the local scan alone would let padding buy a bypass
// of the sidecar.
const maxSidecarWindows = 1024

// errSidecarOversize reports content that needs more than maxSidecarWindows
// sidecar windows.
var errSidecarOversize = errors.New("content too large for sidecar scan")

// sidecarWindowBound is an upper bound on the windows sidecarWindows builds
// for n bytes at limit, computed without building them. Each window after
// the first advances by at least limit minus the overlap minus the bytes
// given up to rune alignment (up to three at the cut and three at the
// overlap start), and always by at least one byte.
func sidecarWindowBound(n, limit int) int {
	advance := max(1, limit-limit/4-2*(utf8.UTFMax-1))
	return 1 + (n+advance-1)/advance
}

// sidecarDetect asks the sidecar about content. Content within
// max_text_length (or with no limit) is one Detect call. Longer content would
// be truncated by the client, leaving its tail unjudged, so it is split into
// overlapping windows no longer than the limit and every window is judged;
// the first injected window decides.
func sidecarDetect(ctx context.Context, g Guard, content string, cfg *Config) (Result, error) {
	limit := 0
	if cfg != nil {
		limit = cfg.MaxTextLength
	}
	if limit <= 0 || len(content) <= limit {
		return g.Detect(ctx, content)
	}
	if bound := sidecarWindowBound(len(content), limit); bound > maxSidecarWindows {
		return Result{}, fmt.Errorf("%w: %d bytes at max_text_length %d need up to %d windows (max %d)", errSidecarOversize, len(content), limit, bound, maxSidecarWindows)
	}
	windows := sidecarWindows(content, limit)
	for i := 0; i < len(windows); i += sidecarBatchSize {
		batch := windows[i:min(i+sidecarBatchSize, len(windows))]
		results, err := g.DetectBatch(ctx, batch)
		if err != nil {
			return Result{}, err
		}
		if len(results) != len(batch) {
			return Result{}, fmt.Errorf("sidecar returned %d results for %d windows", len(results), len(batch))
		}
		for _, r := range results {
			if r.Injected {
				return r, nil
			}
		}
	}
	return Result{Label: "BENIGN"}, nil
}

// sidecarWindows splits content into windows of at most limit bytes that cut
// only at rune boundaries and overlap their neighbour by a quarter of the
// limit, so a phrase straddling a cut is seen whole by at least one window
// when it is shorter than the overlap. A limit shorter than one rune still
// advances by a whole rune.
func sidecarWindows(content string, limit int) []string {
	overlap := limit / 4
	var out []string
	start := 0
	for {
		end := start + limit
		if end >= len(content) {
			return append(out, content[start:])
		}
		for end > start && !utf8.RuneStart(content[end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(content[start:])
			end = start + size
		}
		out = append(out, content[start:end])
		if end == len(content) {
			return out
		}
		next := end - overlap
		for next > start && !utf8.RuneStart(content[next]) {
			next--
		}
		if next <= start {
			next = end
		}
		start = next
	}
}
