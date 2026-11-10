package redact

// Test helpers that run one redaction stage on its own, with the gates on.
// RedactSecrets runs the stages through a shared textScan instead.

// redactCredentials replaces every credential-named assignment in text.
func redactCredentials(text string) string {
	s := newScan(text, false)
	s.redactCredentials()
	return s.text
}

// redactGuarded replaces every guarded match in text, merging overlaps.
func redactGuarded(text string) string {
	s := newScan(text, false)
	s.redactGuarded()
	return s.text
}
