package sandbox

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ErrInvalidNetwork marks a network mode that is not one of the allowed
// values. Callers treat it as a configuration error, never as a reason to
// run without a sandbox.
var ErrInvalidNetwork = errors.New("invalid sandbox network")

// LenientNetworkEnv, when set to "1", restores the older behaviour of
// downgrading an invalid network value to "none" with a warning.
const LenientNetworkEnv = "ODEK_SANDBOX_NETWORK_LENIENT"

// ValidateNetwork returns the effective network mode for network: "" and
// "none" give "none", "bridge" stays "bridge". Any other value yields an
// error wrapping ErrInvalidNetwork that names the value and the allowed ones.
func ValidateNetwork(network string) (string, error) {
	switch network {
	case "", "none":
		return "none", nil
	case "bridge":
		return "bridge", nil
	}
	return "", fmt.Errorf("%w %q: allowed values are \"none\" and \"bridge\" (set %s=1 to downgrade to \"none\" instead of failing)", ErrInvalidNetwork, network, LenientNetworkEnv)
}

// BuildRunArgsChecked validates cfg.Network and then builds the "docker run"
// argument slice. An invalid network is an error (ErrInvalidNetwork) unless
// ODEK_SANDBOX_NETWORK_LENIENT=1, which downgrades it to "none" with a
// stderr warning.
func BuildRunArgsChecked(cfg Config, containerName, workdir, image string) ([]string, error) {
	network, err := ValidateNetwork(cfg.Network)
	if err != nil {
		if os.Getenv(LenientNetworkEnv) != "1" {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "odek: WARNING: --sandbox-network %q is not allowed; %s=1 forces 'none'.\n", cfg.Network, LenientNetworkEnv)
		network = "none"
	}
	cfg.Network = network
	if len(cfg.Networks) > 0 && len(cfg.ResolvedNetworks) != len(cfg.Networks) {
		return nil, fmt.Errorf("%w: attached networks %v were not resolved by docker inspect", ErrInvalidNetwork, cfg.Networks)
	}
	if _, perr := ParsePidsLimit(cfg.PidsLimit); perr != nil {
		return nil, perr
	}
	return BuildRunArgs(cfg, containerName, workdir, image), nil
}

// DefaultPidsLimit is the container process limit applied when no
// sandbox_pids_limit is configured.
const DefaultPidsLimit = 4096

// ErrInvalidPidsLimit marks a pids limit that is not a positive integer.
var ErrInvalidPidsLimit = errors.New("invalid sandbox pids limit")

// ParsePidsLimit returns the effective pids limit for raw: empty gives
// DefaultPidsLimit; anything but a positive decimal integer yields an error
// wrapping ErrInvalidPidsLimit that names the value.
func ParsePidsLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultPidsLimit, nil
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w %q: must be a positive integer", ErrInvalidPidsLimit, raw)
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w %q: must be a positive integer", ErrInvalidPidsLimit, raw)
	}
	return n, nil
}
