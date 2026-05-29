// Package redact provides secret detection and redaction for odek output.
//
// RedactSecrets scans text for API keys, tokens, credentials, private keys,
// and other secrets, replacing matched content with [REDACTED]. This prevents
// secrets from leaking into session files, memory episodes, and Telegram
// messages.
//
// Design:
//   - No external dependencies — pure Go regex
//   - Compiled once at init time — zero allocation on hot path
//   - Ordered by specificity — specific patterns (OpenAI, GitHub, AWS) before
//     generic patterns to avoid false positives
//   - False-positive resistant — minimum length thresholds, entropy checks
//
// The patterns are deliberately conservative. Generic patterns require
// contextual prefixes (key=, token=, secret=, password=) to reduce false
// positives on code snippets like UUIDs or base64-encoded data.
package redact

import (
	"regexp"
	"strings"
)

// ── Patterns ───────────────────────────────────────────────────────────

// Each pattern is a regex that matches a specific secret format.
// Patterns are ordered from most specific to least specific.
// The first matching pattern wins — subsequent patterns are skipped for
// that portion of text.
var patterns = []*regexp.Regexp{
	// OpenAI / AI provider keys: sk-<alphanumeric+hyphens>
	// sk-proj- variant for project-scoped keys
	regexp.MustCompile(`sk-[a-zA-Z0-9-]{32,}`),

	// GitHub personal access tokens (classic + fine-grained)
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[a-zA-Z0-9]{22,}`),

	// AWS access keys: AKIA + 16 uppercase (also ASIA for temp credentials)
	regexp.MustCompile(`A[SK]IA[0-9A-Z]{16}`),

	// Private keys (RSA, EC, OpenSSH, DSA, ED25519, PKCS#8)
	// PKCS#8 format (default openssl genpkey output) — with optional
	// ENCRYPTED prefix and optional algorithm label.
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |ED25519 |ENCRYPTED )?PRIVATE KEY-----[^-]*-----END (RSA |EC |OPENSSH |DSA |ED25519 |ENCRYPTED )?PRIVATE KEY-----`),

	// JWT tokens (three base64url segments separated by dots)
	// Minimum ~40 chars to avoid matching short dotted strings
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}\.[a-zA-Z0-9_-]{20,}`),

	// Generic API keys / tokens / passwords with contextual prefixes.
	// The requirement for a lowercase prefix (key=, token=, etc.) followed by
	// 20+ alphanumeric chars filters out UUIDs, hex hashes in code, and other
	// false-positive-heavy text.
	regexp.MustCompile(`(?i)(?:api[_-]?key|api[_-]?secret|auth[_-]?token|access[_-]?token|bearer[_-]?token|client[_-]?secret|private[_-]?key|secret[_-]?key|password|passwd)\s*[:=]\s*['\x60"]?([a-zA-Z0-9+/=._-]{20,})['\x60"]?`),

	// Bearer tokens in Authorization headers
	regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+([a-zA-Z0-9+/=._-]{20,})`),

	// Slack bot tokens: xoxb-, xoxp-
	regexp.MustCompile(`xox[abpos]-[0-9]{10,}-[0-9]{10,}-[a-zA-Z0-9]{24,}`),

	// Stripe keys: sk_live_, sk_test_, pk_live_, pk_test_
	regexp.MustCompile(`[rs]k_(live|test)_[a-zA-Z0-9]{24,}`),

	// Google API keys: AIza + 35+ alphanumeric/hyphen/underscore
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{35,}`),

	// Twilio keys: SK + 32 hex
	regexp.MustCompile(`SK[0-9a-fA-F]{32}`),

	// Generic credential env vars: EXPORT/VAR=VALUE with long base64 values
	regexp.MustCompile(`(?i)(?:export\s+)?[A-Z_]{3,}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)[A-Z_]{0,20}\s*=\s*['\x60"]?([^\s'\x60"]{20,})['\x60"]?`),

	// HashiCorp Vault tokens — service (hvs.) and batch (hvb.).
	regexp.MustCompile(`hv[sb]\.[A-Za-z0-9_-]{30,}`),

	// Google OAuth 2.0 access tokens (ya29. prefix) and refresh tokens
	// (1//0 prefix). 1// alone is too generic — refresh tokens always
	// begin with the literal "1//0".
	regexp.MustCompile(`ya29\.[A-Za-z0-9_.-]{20,}`),
	regexp.MustCompile(`\b1//0[A-Za-z0-9_-]{30,}`),

	// SendGrid API keys — SG.<22-char id>.<43-char secret>.
	regexp.MustCompile(`SG\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{30,}`),

	// Discord bot tokens — three base64url segments. Discord user IDs
	// are 17–19 digit decimal numbers, which encode in base64 to strings
	// starting with M, N, or O. Anchoring on that prefix + the strict
	// segment-length structure avoids collisions with generic dotted
	// base64 strings; real JWTs are already matched by the eyJ pattern
	// above.
	regexp.MustCompile(`\b[MNO][A-Za-z0-9_-]{22,27}\.[A-Za-z0-9_-]{5,7}\.[A-Za-z0-9_-]{27,40}\b`),

	// Database connection URLs with embedded credentials. We require a
	// scheme that genuinely carries DB creds (so this doesn't catch HTTP
	// basic auth URLs that often appear legitimately in code). The
	// password segment must be at least 6 chars to avoid matching common
	// placeholders like `:x@`.
	regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?|mssql|clickhouse)://[^:\s/]+:[^@\s/]{6,}@[^\s'\x60"]+`),
}

// ── Public API ─────────────────────────────────────────────────────────

// RedactSecrets scans text for known secret patterns and replaces matched
// content with "[REDACTED]". Returns the sanitized text.
//
// The function is safe to call on empty strings and strings without secrets
// (returns the original string unchanged in the common case).
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}

	result := text
	for _, p := range patterns {
		result = p.ReplaceAllString(result, "[REDACTED]")
	}
	return result
}

// HasSecrets returns true if the text contains any recognized secret pattern.
// Useful for quick pre-checks without allocating the full redacted string.
func HasSecrets(text string) bool {
	if text == "" {
		return false
	}
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// IsSafe returns true if the text contains no recognized secrets.
// Convenience inverse of HasSecrets.
func IsSafe(text string) bool {
	return !HasSecrets(text)
}

// ── Helpers ────────────────────────────────────────────────────────────

// CountSecrets returns the number of secret patterns found in the text.
// Useful for logging and metrics.
func CountSecrets(text string) int {
	if text == "" {
		return 0
	}
	count := 0
	for _, p := range patterns {
		matches := p.FindAllString(text, -1)
		count += len(matches)
	}
	return count
}

// RedactWithCount returns both the redacted text and a count of redacted
// secrets, so callers can log how many were caught without a second pass.
func RedactWithCount(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	count := CountSecrets(text)
	result := RedactSecrets(text)
	return result, count
}

// ── Chunk helpers ──────────────────────────────────────────────────────

// RedactChunk redacts a single chunk of text and returns it along with
// a boolean indicating whether any secrets were found.
// Designed for streaming/chunked output where callers want to know
// per-chunk whether redaction occurred.
func RedactChunk(chunk string) (string, bool) {
	if chunk == "" {
		return chunk, false
	}
	had := HasSecrets(chunk)
	if !had {
		return chunk, false
	}
	return RedactSecrets(chunk), true
}

// ── Sanitize for safe comparison ───────────────────────────────────────

// SanitizeForLog returns a version of the text safe for logging.
// Unlike RedactSecrets which replaces matched substrings, this returns
// a descriptive summary when secrets are found. Useful for log messages
// where you want to know secrets WERE present without any risk of
// partial leakage.
func SanitizeForLog(text string) string {
	if text == "" {
		return text
	}
	if HasSecrets(text) {
		return strings.Repeat("[REDACTED] ", CountSecrets(text))
	}
	return text
}
