package main

import (
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// With the Docker daemon reachable, a failure to resolve the image or to
// start the container comes from the configuration (image, limits, user,
// networks) and must stop the run, even when the sandbox was not requested
// explicitly. Only an unreachable daemon keeps the loud host fallback.

func stubDockerReachable(t *testing.T, up bool) {
	t.Helper()
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	old := dockerReachable
	dockerReachable = func() bool { return up }
	t.Cleanup(func() { dockerReachable = old })
}

func TestRED_EnsureSandbox_StartFailureWithDaemonUpIsFatal(t *testing.T) {
	stubDockerReachable(t, true)
	stubSandboxImage(t, true, nil) // the image is cached, so docker run fails on the config alone
	for _, cfg := range []sandboxConfig{
		{Image: "-bad"},                     // invalid image reference
		{Image: "alpine:3", Memory: "lots"}, // docker run rejects the limit
	} {
		_, _, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, cfg)
		if sandboxed || err == nil || !errors.Is(err, errSandboxConfig) {
			t.Errorf("cfg %+v: want a fatal sandbox configuration error, got sandboxed=%v err=%v", cfg, sandboxed, err)
		}
	}
}

func TestRED_EnsureSandbox_DaemonDownKeepsImplicitFallback(t *testing.T) {
	stubDockerReachable(t, false)
	_, _, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{Image: "-bad"})
	if sandboxed || err != nil {
		t.Errorf("an unreachable daemon in implicit mode keeps the loud fallback, got sandboxed=%v err=%v", sandboxed, err)
	}
}
