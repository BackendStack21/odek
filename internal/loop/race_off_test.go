//go:build !race

package loop

// raceEnabled reports that the test binary runs without the race detector;
// timing bounds apply unscaled.
const raceEnabled = false
