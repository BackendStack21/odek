//go:build !race

package memory

// raceEnabled reports that the test binary runs without the race detector.
const raceEnabled = false
