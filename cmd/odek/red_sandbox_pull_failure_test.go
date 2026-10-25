package main

import (
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
)

// An image that is not cached and cannot be pulled (offline laptop,
// registry outage) is not a configuration error: implicit mode keeps the
// loud fallback even though the daemon answers.

func stubSandboxImage(t *testing.T, present bool, pullErr error) {
	t.Helper()
	oldPresent, oldPull := sandboxImagePresent, sandboxPullImage
	sandboxImagePresent = func(string) bool { return present }
	sandboxPullImage = func(string) error { return pullErr }
	t.Cleanup(func() { sandboxImagePresent, sandboxPullImage = oldPresent, oldPull })
}

func TestRED_EnsureSandbox_PullFailureKeepsImplicitFallback(t *testing.T) {
	stubDockerReachable(t, true)
	stubSandboxImage(t, false, errors.New("dial tcp: lookup registry-1.docker.io: no such host"))
	_, _, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{Image: "alpine:3"})
	if sandboxed || err != nil {
		t.Errorf("a pull failure must keep the implicit fallback, got sandboxed=%v err=%v", sandboxed, err)
	}
}

func TestRED_EnsureSandbox_PullFailureExplicitIsFatalButNotConfig(t *testing.T) {
	stubDockerReachable(t, true)
	stubSandboxImage(t, false, errors.New("registry unreachable"))
	_, _, _, err := ensureSandbox(config.ResolvedConfig{Sandbox: true, SandboxExplicit: true}, nil, sandboxConfig{Image: "alpine:3"})
	if err == nil || errors.Is(err, errSandboxConfig) {
		t.Errorf("explicit --sandbox with a pull failure must fail, but not as a configuration error: %v", err)
	}
}
