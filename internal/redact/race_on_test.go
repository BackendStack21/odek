//go:build race

package redact

// raceEnabled reports that the test binary runs under the race detector,
// which slows the scanners by an order of magnitude; absolute timing bounds
// are skipped.
const raceEnabled = true
