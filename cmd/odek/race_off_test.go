//go:build !race

package main

// raceEnabled reports whether the test binary was built with -race.
// Used by tests that exercise tight Process.Kill()/Run() interleavings
// that the race detector flags as inherent (not actual data races).
const raceEnabled = false
