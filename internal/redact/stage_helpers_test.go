package redact

import "testing"

// Test helpers that run one redaction stage on its own, with the gates on.
// RedactSecrets runs the stages through a shared textScan instead.

// leanRun reports a short run under the race detector, as CI runs the
// package: the scanners are an order of magnitude slower there, so the
// generated part of a corpus is cut down and the fixed part and the invariant
// are kept.
func leanRun() bool {
	return raceEnabled && testing.Short()
}

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
