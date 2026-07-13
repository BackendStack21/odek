package memory

import (
	"context"
	"fmt"
	"regexp"

	"github.com/BackendStack21/odek/internal/guard"
)

// ScanContent checks memory content for security threats. Returns an error if
// the content contains patterns that could compromise the agent.
//
// Checks:
//   - Invisible Unicode characters (zero-width spaces, direction overrides, BOM)
//   - Mixed confusable scripts (Cyrillic/Greek homoglyphs mixed with Latin)
//   - Prompt injection markers ("ignore previous instructions", etc.)
//   - Credential exfiltration patterns (API keys, private keys, bearer tokens)
func ScanContent(content string) error {
	if err := guard.ScanContent(context.Background(), content, nil, nil); err != nil {
		return fmt.Errorf("memory: %v", err)
	}
	return nil
}

// remoteExecRe / evalFetchRe match the download-and-execute / pipe-to-shell
// class of instruction — the shape a poisoned "fact" would take to turn the
// always-injected fact files into a persistent backdoor. They are deliberately
// NARROW: legitimate command facts a session should remember (e.g. "go test
// ./...", "make build", "uses Postgres on :5432") do not match — only a remote
// fetch piped into a shell, or eval/source of a fetched command, do.
var (
	remoteExecRe = regexp.MustCompile(`(?i)\b(curl|wget|fetch|iwr|invoke-webrequest)\b[^\n|]*\|\s*\w*sh\b`)
	evalFetchRe  = regexp.MustCompile(`(?i)\b(eval|exec|source)\b[^\n]*\$\(\s*(curl|wget|fetch)\b`)
)

// FactLooksUnsafe reports whether a fact embeds a download-and-execute /
// pipe-to-shell instruction. It is applied ONLY to AUTO-extracted facts (which
// are lower-trust and injected into every system prompt), not to facts the user
// adds explicitly via the memory tool. It does not catch every malicious fact —
// turning conversation into durable memory has an irreducible residual risk —
// but it closes the concrete download-and-run class.
func FactLooksUnsafe(fact string) bool {
	return remoteExecRe.MatchString(fact) || evalFetchRe.MatchString(fact)
}
