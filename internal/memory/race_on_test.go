//go:build race

package memory

// raceEnabled reports that the test binary runs under the race detector,
// which slows the regexp engine by an order of magnitude; scaling tests use
// smaller inputs.
const raceEnabled = true
