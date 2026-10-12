//go:build !race

package redact

// raceEnabled reports that the test binary runs without the race detector.
const raceEnabled = false
