package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	odek "github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
)

func stubSetupSandbox(t *testing.T, serr error) {
	t.Helper()
	orig := setupSandboxFn
	t.Cleanup(func() { setupSandboxFn = orig })
	setupSandboxFn = func(tools []odek.Tool, cfg sandboxConfig) (string, func() error, error) {
		return "", nil, serr
	}
}

func TestEnsureSandbox_ImplicitConfigErrorIsFatal(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	stubSetupSandbox(t, fmt.Errorf("bad network: %w", errSandboxConfig))

	flush := captureStderr(t)
	name, cleanup, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{})
	stderr := flush()

	if err == nil {
		t.Fatal("config error in implicit mode must be fatal")
	}
	if !errors.Is(err, errSandboxConfig) {
		t.Errorf("error must wrap errSandboxConfig: %v", err)
	}
	if sandboxed || name != "" || cleanup != nil {
		t.Errorf("no sandbox may be reported: %q %v", name, sandboxed)
	}
	if strings.Contains(stderr, "WITHOUT sandbox") {
		t.Errorf("must not print the host-fallback warning:\n%s", stderr)
	}
}

func TestEnsureSandbox_ImplicitDockerUnavailableStillFallsBack(t *testing.T) {
	t.Setenv("ODEK_NO_SANDBOX", "")
	t.Setenv("ODEK_REQUIRE_SANDBOX", "")
	stubSetupSandbox(t, errors.New("docker daemon not reachable"))

	flush := captureStderr(t)
	name, cleanup, sandboxed, err := ensureSandbox(config.ResolvedConfig{}, nil, sandboxConfig{})
	stderr := flush()

	if err != nil {
		t.Fatalf("non-config error must degrade in implicit mode: %v", err)
	}
	if sandboxed || name != "" || cleanup != nil {
		t.Errorf("degraded run must not report a sandbox")
	}
	if !strings.Contains(stderr, "WITHOUT sandbox") {
		t.Errorf("fallback warning expected:\n%s", stderr)
	}
}
