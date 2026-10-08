//go:build !race

package danger

// raceEnabled reports that the test binary runs without the race detector;
// timing bounds apply unscaled.
const raceEnabled = false
