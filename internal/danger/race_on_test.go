//go:build race

package danger

// raceEnabled reports that the test binary runs under the race detector,
// whose instrumentation (and atomic coverage counters alongside it) slows
// the classifier by an order of magnitude or more. Timing bounds that guard
// against algorithmic blowups scale by slowFactor so a legitimately linear
// analysis is not reported as a hang.
const raceEnabled = true
