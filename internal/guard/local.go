package guard

import (
	"context"
	"regexp"
	"time"

	"github.com/BackendStack21/odek/internal/danger"
)

// localGuard is the zero-dependency rule-based guard.
// It reuses the hardened danger.ScanInjection classifier plus credential checks.
type localGuard struct{}

// localGuardInstance is the shared local-guard implementation so ScanContent
// can avoid allocating a new guard on every call.
var localGuardInstance = &localGuard{}

// NewLocalGuard creates a guard backed by danger.ScanInjection.
func NewLocalGuard() Guard {
	return localGuardInstance
}

// Detect runs the local rule-based scan.
func (l *localGuard) Detect(ctx context.Context, text string) (Result, error) {
	start := time.Now()
	if threats := danger.ScanInjection(text); len(threats) > 0 {
		return Result{
			Label:    threats[0].Label,
			Score:    1.0,
			Injected: true,
			Latency:  time.Since(start),
		}, nil
	}
	if hasCredentials(text) {
		return Result{
			Label:    "potential credential material",
			Score:    1.0,
			Injected: true,
			Latency:  time.Since(start),
		}, nil
	}
	return Result{
		Label:    "BENIGN",
		Score:    0.0,
		Injected: false,
		Latency:  time.Since(start),
	}, nil
}

// DetectBatch scans each text independently. The local guard does not gain
// latency from batching, but it honors the same interface as the piguard client.
func (l *localGuard) DetectBatch(ctx context.Context, texts []string) ([]Result, error) {
	results := make([]Result, len(texts))
	for i, text := range texts {
		r, err := l.Detect(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = r
	}
	return results, nil
}

// DetectLong is equivalent to Detect for the local guard; the rule-based scan
// is not window-limited.
func (l *localGuard) DetectLong(ctx context.Context, text string) (Result, error) {
	return l.Detect(ctx, text)
}

// Close is a no-op for the local guard.
func (l *localGuard) Close() error { return nil }

var (
	reSKKey       = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{20,}\b`)
	rePrivateKey  = regexp.MustCompile(`-----BEGIN\s+(?:RSA|DSA|EC|OPENSSH|PGP)\s+PRIVATE\s+KEY`)
	reBearerToken = regexp.MustCompile(`(?i)\bbearer\s+[a-zA-Z0-9._-]{20,}\b`)
)

// hasCredentials checks for patterns that look like leaked secrets.
func hasCredentials(s string) bool {
	if reSKKey.MatchString(s) {
		return true
	}
	if rePrivateKey.MatchString(s) {
		return true
	}
	if reBearerToken.MatchString(s) {
		return true
	}
	return false
}
