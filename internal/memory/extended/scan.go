package extended

import (
	"fmt"
	"regexp"

	"github.com/BackendStack21/odek/internal/danger"
)

// ScanContent checks atom content for security threats. It mirrors the checks
// in the parent memory package so Extended Memory can validate atoms without
// creating an import cycle.
func ScanContent(content string) error {
	if danger.ContainsInvisible(content) {
		return fmt.Errorf("extended memory: content contains invisible Unicode characters")
	}
	if danger.HasConfusableScript(content) {
		return fmt.Errorf("extended memory: content contains mixed confusable scripts")
	}
	if threats := danger.ScanInjection(content); len(threats) > 0 {
		return fmt.Errorf("extended memory: content contains injection pattern: %q", threats[0].Label)
	}
	if hasCredentials(content) {
		return fmt.Errorf("extended memory: content contains potential credential material")
	}
	return nil
}

var (
	reSKKey       = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{20,}\b`)
	rePrivateKey  = regexp.MustCompile(`-----BEGIN\s+(?:RSA|DSA|EC|OPENSSH|PGP)\s+PRIVATE\s+KEY`)
	reBearerToken = regexp.MustCompile(`(?i)\bbearer\s+[a-zA-Z0-9._-]{20,}\b`)
)

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
