//go:build race

package loop

// raceEnabled reports that the test binary runs under the race detector,
// whose instrumentation (and atomic coverage counters alongside it) slows
// the neutraliser by an order of magnitude or more. Absolute timing bounds
// are skipped under it; scaling checks still run.
const raceEnabled = true
