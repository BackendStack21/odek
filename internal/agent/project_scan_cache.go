package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/BackendStack21/odek/internal/guard"
)

// scanProjectContent is the injection scan applied to AGENTS.md content; a
// variable so tests can count invocations.
var scanProjectContent = guard.ScanContentWithScope

// maxProjectScanCache bounds the number of remembered accepted verdicts.
const maxProjectScanCache = 64

var (
	projectScanMu       sync.Mutex
	projectScanAccepted = map[string]struct{}{}
)

// scanProjectFile scans AGENTS.md content for prompt injection. Accepted
// verdicts are remembered for the life of the process, keyed by the content's
// SHA-256 and the guard instance and configuration that accepted it, so
// repeated agent construction (sub-agents, REST runs) does not rescan an
// unchanged file. Rejections are never cached and always rescanned; any change
// to the content or to the guard setup misses the cache and is scanned afresh.
func scanProjectFile(content string, cfg *Config) error {
	key, cacheable := projectScanKey(content, cfg)
	if cacheable {
		projectScanMu.Lock()
		_, ok := projectScanAccepted[key]
		projectScanMu.Unlock()
		if ok {
			return nil
		}
	}
	if err := scanProjectContent(context.Background(), content, cfg.Guard, &cfg.GuardConfig, "system_prompt"); err != nil {
		return err
	}
	if cacheable {
		projectScanMu.Lock()
		if len(projectScanAccepted) >= maxProjectScanCache {
			projectScanAccepted = map[string]struct{}{}
		}
		projectScanAccepted[key] = struct{}{}
		projectScanMu.Unlock()
	}
	return nil
}

// projectScanKey derives the cache key. Only the local rule scanner is
// cacheable: its verdict depends on the text and the scope switch alone. A
// sidecar provider is never cached, so a sidecar outage that fell back to the
// local rules cannot be remembered as a lasting acceptance, and the key is
// built from values, never from pointer addresses that a later allocation
// could reuse.
func projectScanKey(content string, cfg *Config) (string, bool) {
	// A nil guard means ScanContent runs the local rules alone.
	if p := cfg.GuardConfig.Provider; cfg.Guard != nil && p != "" && p != guard.ProviderLocal {
		return "", false
	}
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x|%t", sum, guard.IsEnabled(cfg.GuardConfig.Scan, "system_prompt")), true
}
